// Package proxyguard implements the library part of ProxyGuard
package proxyguard

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Client represents a ProxyGuard client
type Client struct {
	// Listen is the PORT for the UDP listener
	ListenPort int

	// TCPSourcePort is the source port for the TCP connection
	TCPSourcePort int

	// uconn is the udp connection
	uconn *net.UDPConn

	// Fwmark sets the SO_MARK to use
	// Set to 0 or negative to disable
	// This is only set on Linux
	Fwmark int

	// Peer is the peer to connect to
	Peer string

	// PeerIPS is the list of DNS resolved IPs for the Peer
	// You may leave this empty and automatically resolve PeerIPs using
	// `Setup`
	PeerIPS []string

	// SetupSocket is called when the socket is setting up
	// fd is the file descriptor of the socket
	SetupSocket func(fd int)

	// UserAgent is the HTTP user agent to use for HTTP requests
	UserAgent string
}

func (c *Client) setupDNS(ctx context.Context) error {
	// peer IPs already resolved
	if len(c.PeerIPS) > 0 {
		return nil
	}
	u, err := url.Parse(c.Peer)
	if err != nil {
		return err
	}

	gpips, err := net.DefaultResolver.LookupHost(ctx, u.Hostname())
	if err != nil {
		return err
	}
	c.PeerIPS = gpips
	return nil
}

// Setup makes sure the client has a UDP socket and caches the DNS for the Proxy endpoint
// It returns the UDP port the client is listening on, this is useful to know if the Client's ListenPort was left on zero
// This function has to be called before `Tunnel`
// When a client is done it has to call `Close` if this function has returned no error
func (c *Client) Setup(ctx context.Context) (int, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: c.ListenPort,
	})
	if err != nil {
		return -1, err
	}
	err = c.setupDNS(ctx)
	if err != nil {
		clErr := conn.Close()
		if clErr != nil {
			log.Logf("failed closing UDP conn: %v", clErr)
		}
		return -1, err
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	c.uconn = conn
	return port, nil
}

// Close closes the underlying UDP connection to WireGuard
func (c *Client) Close() {
	if c.uconn == nil {
		return
	}
	clErr := c.uconn.Close()
	if clErr != nil {
		log.Logf("failed closing client: %v", clErr)
	}
	c.uconn = nil
}

// Tunnel tunnels a connection from wireguard connection port `wglisten`
// This has to be called after 'Setup'
// If this function returns, you might want to call `Close`
func (c *Client) Tunnel(ctx context.Context, wglisten int) error {
	if c.uconn == nil {
		return errors.New("no UDP connection, was Setup called?")
	}
	// If the tunnel exits within this delta, that restart is marked as 'failed'
	// and the next wait is cycled through
	d := time.Duration(10 * time.Second)

	// the times to wait, the restartUntilErr function loops through these
	wt := []time.Duration{
		time.Duration(1 * time.Second),
		time.Duration(2 * time.Second),
		time.Duration(4 * time.Second),
		time.Duration(8 * time.Second),
		time.Duration(10 * time.Second),
	}
	err := restartUntilErr(ctx, func(ctx context.Context) error {
		log.Logf("waiting for traffic...")
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		err := c.tryTunnel(cctx, wglisten)
		var fErr *fatalError
		if errors.As(err, &fErr) {
			log.Logf("%v, exiting...", fErr)
			return fErr
		}
		if err != nil {
			log.Logf("Retrying as client exited with error: %v", err)
		} else {
			log.Logf("Retrying as client exited cleanly but context is not canceled yet")
		}
		return nil
	}, wt, d)
	return err
}

type fatalError struct {
	Err error
}

func (fe *fatalError) Error() string {
	return fmt.Sprintf("fatal error occurred: %v", fe.Err.Error())
}

// tcpHandshake is a wrapper around a net.TCPConn and a bufio.ReadWriter
// that (re)does a HTTP upgrade if the connection has previously been shutdown to e.g. an idle timeout
// or when it still needs to be established
// It does this by only re-establishing when a write is called
type tcpHandshake struct {
	ctx         context.Context
	peer        string
	pips        []string
	fwmark      int
	sourcePort  int
	setupSocket func(fd int)
	userAgent   string
	httpc       *http.Client
	tr          *timeoutReader
	wc          io.WriteCloser
	wg          sync.WaitGroup
	established bool
}

// configureSocket creates a TCP dial with fwmark/SO_MARK set
// it also calls the GotClientFD updater
func (th *tcpHandshake) configureSocket() net.Dialer {
	d := net.Dialer{
		Control: func(_, _ string, conn syscall.RawConn) error {
			var seterr error
			err := conn.Control(func(fd uintptr) {
				if th.sourcePort > 0 && runtime.GOOS == "linux" {
					// if we fail to set the reuse port option
					// it is fine, we only log
					sporterr := socketReuseSport(int(fd))
					if sporterr != nil {
						log.Logf("error re-using source port: %v", sporterr)
					}
				}
				if th.fwmark > 0 && runtime.GOOS == "linux" {
					seterr = socketFWMark(int(fd), th.fwmark)
				}
				if th.setupSocket != nil {
					th.setupSocket(int(fd))
				}
			})
			if err != nil {
				return err
			}
			return seterr
		},
		LocalAddr: &net.TCPAddr{
			Port: th.sourcePort,
		},
		Timeout: 10 * time.Second,
	}
	return d
}

func dialContext(ctx context.Context, dialer net.Dialer, network string, addr string, peerhost string, pips []string) (conn net.Conn, err error) {
	// no peer ips defined
	// just do the dial context with the configured dialer
	if len(pips) == 0 {
		return dialer.DialContext(ctx, network, addr)
	}

	// there are hardcoded ips given
	// use that instead of a DNS request

	// the address is given in hostname:port
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, &fatalError{Err: err}
	}

	// the hostname is not the peer hostname
	// return the default dialcontext
	if host != peerhost {
		log.Logf("host: %s, not equal to peer host: %s, not using DNS cache...", host, peerhost)
		return dialer.DialContext(ctx, network, addr)
	}

	// otherwise loop over the ips and return if one succeeds
	for _, ip := range pips {
		conn, err = dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return conn, nil
		}
		log.Logf("dialing: '%s' failed with ip: '%s', error: %v", host, ip, err)
	}
	return conn, err
}

func (th *tcpHandshake) Handshake() error {
	log.Log("Connecting to HTTP server...")
	u, err := url.Parse(th.peer)
	if err != nil {
		return &fatalError{Err: err}
	}

	// get the hostname of the peer without the port
	peerhost := u.Hostname()
	// set fwmark on the socket
	dialer := th.configureSocket()
	if th.httpc == nil {
		th.httpc = &http.Client{}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// disable HTTP/2 as connection upgrades do not support this
	// According to go docs (https://pkg.go.dev/net/http): Programs that must disable HTTP/2
	// can do so by setting [Transport.TLSNextProto] (for clients)
	// or [Server.TLSNextProto] (for servers) to a non-nil, empty map
	transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	transport.DialContext = func(ctx context.Context, network string, addr string) (conn net.Conn, err error) {
		return dialContext(ctx, dialer, network, addr, peerhost, th.pips)
	}
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	th.httpc.Transport = transport

	req, err := http.NewRequestWithContext(th.ctx, "GET", th.peer, nil)
	if err != nil {
		return &fatalError{Err: err}
	}
	if th.userAgent != "" {
		req.Header.Add("User-Agent", th.userAgent)
	}

	// upgrade the connection to UDP over TCP
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", UpgradeProto)

	resp, err := th.httpc.Do(req)
	if err != nil {
		return err
	}
	// TODO: why does nhooyr.io/websocket set the body to nil and make a rb copy?
	// is this needed?
	rb := resp.Body
	resp.Body = nil

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return &fatalError{Err: fmt.Errorf("status is not switching protocols, got: '%v'", resp.StatusCode)}
	}

	if !strings.EqualFold(resp.Header.Get("Connection"), "Upgrade") {
		return &fatalError{Err: fmt.Errorf("the 'Connection' header is not 'Upgrade', got: '%v'", resp.Header.Get("Connection"))}
	}

	if !strings.EqualFold(resp.Header.Get("Upgrade"), UpgradeProto) {
		return &fatalError{Err: fmt.Errorf("upgrade header is not '%v', got: '%v'", UpgradeProto, resp.Header.Get("Upgrade"))}
	}

	rwc, ok := rb.(io.ReadWriteCloser)
	if !ok {
		return &fatalError{Err: fmt.Errorf("response body is not of type io.ReadWriteCloser: %T", rb)}
	}
	th.wc = rwc
	th.tr = newTimeoutReader(th.ctx, rwc, 60*time.Second)
	th.established = true
	th.wg.Done()

	log.Logf("Connected to HTTP server, ready for proxying traffic...")
	return nil
}

func (th *tcpHandshake) Read(p []byte) (n int, err error) {
	// TODO: how expensive is all of this?
	// Ideally we only want to do this when not established yet

	// wait for the handshake to have completed
	// or for the context to be canceled
	done := make(chan struct{}, 1)
	go func() {
		th.wg.Wait()
		close(done)
	}()
	select {
	case <-th.ctx.Done():
		return 0, context.Canceled
	case <-done:
		// this space intentionally left blank
	}
	if th.tr == nil {
		return 0, io.EOF
	}
	return th.tr.Read(p)
}

func (th *tcpHandshake) Close() {
	if th.wc != nil {
		clErr := th.wc.Close()
		if clErr != nil {
			log.Logf("failed closing handshake: %v", clErr)
		}
		th.wc = nil
		th.tr = nil
	}
	// if we didn't do a handshake yet
	// we signal that the waitgroup is done
	// such that in read we do not get stuck in a goroutine
	if !th.established {
		th.wg.Done()
	}
}

func (th *tcpHandshake) Write(p []byte) (n int, err error) {
	if !th.established {
		log.Logf("Got traffic, creating a handshake...")
		herr := th.Handshake()
		if herr != nil {
			return 0, herr
		}
	}
	if th.wc == nil {
		return 0, io.EOF
	}
	return th.wc.Write(p)
}

// tryTunnel tries to tunnel the connection by connecting to HTTP peer `to` with IPs `pips`
// the boolean `first` is set if it's the first connect to the server
func (c *Client) tryTunnel(ctx context.Context, wglisten int) (err error) {
	th := &tcpHandshake{
		ctx:         ctx,
		peer:        c.Peer,
		pips:        c.PeerIPS,
		fwmark:      c.Fwmark,
		sourcePort:  c.TCPSourcePort,
		setupSocket: c.SetupSocket,
		userAgent:   c.UserAgent,
	}
	th.wg.Add(1)
	defer th.Close()
	wgaddr := &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: wglisten,
	}
	rw := bufio.NewReadWriter(bufio.NewReader(th), bufio.NewWriter(th))
	return tunnel(ctx, c.uconn, rw, wgaddr)
}
