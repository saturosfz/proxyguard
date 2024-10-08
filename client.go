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

	// Fwmark sets the SO_MARK to use
	// Set to 0 or negative to disable
	// This is only set on Linux
	Fwmark int

	// Peer is the peer to connect to
	Peer string

	// PeerIPS is the list of DNS resolved IPs for the Peer
	// You may leave this empty and automatically resolve PeerIPs using
	// `SetupDNS`
	PeerIPS []string

	// SetupSocket is called when the socket is setting up
	// fd is the file descriptor of the socket
	SetupSocket func(fd int)

	// UserAgent is the HTTP user agent to use for HTTP requests
	UserAgent string
}

func (c *Client) SetupDNS(ctx context.Context) error {
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

// Tunnel tunnels a connection from wireguard connection port `wglisten`
func (c *Client) Tunnel(ctx context.Context, wglisten int) error {
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
		err := c.tryTunnel(ctx, wglisten)
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
	rwc         io.ReadWriteCloser
	established bool
	mu          sync.RWMutex
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

	peerhost := u.Host

	// set fwmark on the socket
	dialer := th.configureSocket()
	if th.httpc == nil {
		th.httpc = &http.Client{}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
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
	th.rwc = rwc
	th.mu.Lock()
	th.established = true
	th.mu.Unlock()
	log.Logf("Connected to HTTP server, ready for proxying traffic...")
	return nil
}

func (th *tcpHandshake) Read(p []byte) (n int, err error) {
	th.mu.RLock()
	if !th.established {
		th.mu.RUnlock()
		return 0, nil
	}
	th.mu.RUnlock()
	return th.rwc.Read(p)
}

func (th *tcpHandshake) Close() {
	if th.rwc != nil {
		th.rwc.Close()
	}
}

func (th *tcpHandshake) Write(p []byte) (n int, err error) {
	th.mu.RLock()
	if !th.established {
		th.mu.RUnlock()
		log.Logf("Got traffic, creating a handshake...")
		herr := th.Handshake()
		if herr != nil {
			return 0, herr
		}
	} else {
		th.mu.RUnlock()
	}
	return th.rwc.Write(p)
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
	defer th.Close()
	udpaddr := &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: c.ListenPort,
	}
	wgaddr := &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: wglisten,
	}
	wgconn, err := net.DialUDP("udp", udpaddr, wgaddr)
	if err != nil {
		return err
	}
	defer wgconn.Close()
	rw := bufio.NewReadWriter(bufio.NewReader(th), bufio.NewWriter(th))
	return tunnel(ctx, wgconn, rw)
}
