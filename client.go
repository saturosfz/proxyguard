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
	"syscall"
	"time"
)

// Client represents a ProxyGuard client
type Client struct {
	// Listen is the PORT for the UDP listener
	ListenPort int

	// ForwardPort is the PORT from which the UDP traffic originates
	ForwardPort int

	// TCPSourcePort is the source port for the TCP connection
	TCPSourcePort int

	// Fwmark sets the SO_MARK to use
	// Set to 0 or negative to disable
	// This is only set on Linux
	Fwmark int

	// Ready is the callback that is called when the Proxy is connected to the peer
	// In this callback, the callback should start WireGuard if not started yet
	// This callback returns the port that WireGuard is listening on
	// Which will set the `ForwardPort` if only and if it is not set yet
	Ready func() (int, error)

	// SetupSocket is called when the socket is setting up
	// fd is the file descriptor of the socket
	// pips are the ips of the peer that the socket will attempt to connect to
	SetupSocket func(fd int, pips []string)

	// UserAgent is the HTTP user agent to use for HTTP requests
	UserAgent string

	// httpc is the cached HTTP client
	httpc *http.Client
}

// configureSocket creates a TCP dial with fwmark/SO_MARK set
// it also calls the GotClientFD updater
func (c *Client) configureSocket(pips []string) net.Dialer {
	d := net.Dialer{
		Control: func(_, _ string, conn syscall.RawConn) error {
			var seterr error
			err := conn.Control(func(fd uintptr) {
				if c.TCPSourcePort > 0 && runtime.GOOS == "linux" {
					// if we fail to set the reuse port option
					// it is fine, we only log
					sporterr := socketReuseSport(int(fd))
					if sporterr != nil {
						log.Logf("error re-using source port: %v", sporterr)
					}
				}
				if c.Fwmark > 0 && runtime.GOOS == "linux" {
					seterr = socketFWMark(int(fd), c.Fwmark)
				}
				if c.SetupSocket != nil {
					c.SetupSocket(int(fd), pips)
				}
			})
			if err != nil {
				return err
			}
			return seterr
		},
		LocalAddr: &net.TCPAddr{
			Port: c.TCPSourcePort,
		},
		Timeout: 10 * time.Second,
	}
	return d
}

// Tunnel tunnels a connection to peer `peer`
// The peer has IP addresses `pips`, if empty a DNS request is done
func (c *Client) Tunnel(ctx context.Context, peer string, pips []string) error {
	// do a DNS request and fill peer IPs
	// if none are given
	if len(pips) == 0 {
		u, err := url.Parse(peer)
		if err != nil {
			return err
		}

		gpips, err := net.DefaultResolver.LookupHost(ctx, u.Hostname())
		if err != nil {
			return err
		}
		pips = gpips
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

	err := restartUntilErr(ctx, func(ctx context.Context, first bool) error {
		err := c.tryTunnel(ctx, peer, pips, first)

		// if a fatal error is returned, exit immediately
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

func (c *Client) dialContext(ctx context.Context, dialer net.Dialer, network string, addr string, peerhost string, pips []string) (conn net.Conn, err error) {
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

type fatalError struct {
	Err error
}

func (fe *fatalError) Error() string {
	return fmt.Sprintf("fatal error occurred: %v", fe.Err.Error())
}

// tryTunnel tries to tunnel the connection by connecting to HTTP peer `to` with IPs `pips`
// the boolean `first` is set if it's the first connect to the server
func (c *Client) tryTunnel(ctx context.Context, peer string, pips []string, first bool) (err error) {
	log.Log("Connecting to HTTP server...")
	u, err := url.Parse(peer)
	if err != nil {
		return &fatalError{Err: err}
	}

	peerhost := u.Host

	// set fwmark on the socket
	dialer := c.configureSocket(pips)
	if c.httpc == nil {
		c.httpc = &http.Client{}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network string, addr string) (conn net.Conn, err error) {
		return c.dialContext(ctx, dialer, network, addr, peerhost, pips)
	}
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	c.httpc.Transport = transport

	req, err := http.NewRequestWithContext(ctx, "GET", peer, nil)
	if err != nil {
		return &fatalError{Err: err}
	}
	if c.UserAgent != "" {
		req.Header.Add("User-Agent", c.UserAgent)
	}

	// upgrade the connection to UDP over TCP
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", UpgradeProto)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	// TODO: why does nhooyr.io/websocket set the body to nil and make a rb copy?
	// is this needed?
	rb := resp.Body
	resp.Body = nil
	defer rb.Close()

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
	log.Log("Connected to HTTP server")

	if c.Ready != nil && first {
		// This calls the ready callback
		// Which means the client can begin setting up wireguard
		// This callback should return the port WireGuard is listening on
		fwp, err := c.Ready() 
		if err != nil {
			return &fatalError{Err: fmt.Errorf("Client ready callback gave an error: %w", err)}
		}
		if c.ForwardPort == 0 {
			c.ForwardPort = fwp
		}
	}

	udpaddr := &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
		Port: c.ListenPort,
	}
	wgaddr := &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
		Port: c.ForwardPort,
	}
	wgconn, err := net.DialUDP("udp", udpaddr, wgaddr)
	if err != nil {
		return err
	}
	defer wgconn.Close()
	log.Log("Client is ready for converting UDP<->HTTP")

	// create a buffered read writer
	rw := bufio.NewReadWriter(bufio.NewReader(rwc), bufio.NewWriter(rwc))

	return tunnel(ctx, wgconn, rw)
}
