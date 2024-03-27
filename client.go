package proxyguard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Client represents a Proxyguard client
type Client struct {
	// Listen is the IP:PORT for the UDP listener
	Listen string

	// TCPSourcePort is the source port for the TCP connection
	TCPSourcePort int

	// Fwmark sets the SO_MARK to use
	// Set to 0 or negative to disable
	// This is only set on Linux
	Fwmark int

	// Ready is the callback that is called when the Proxy is connected to the peer
	// This only gets called on first connect of `Tunnel`
	Ready func()

	// SetupSocket is called when the socket is setting up
	// fd is the file descriptor of the socket
	// pips are the ips of the peer that the socket will attempt to connect to
	SetupSocket func(fd int, pips []string)

	// httpc is the cached HTTP client
	httpc *http.Client

	// resFunc is the function to call when the client should restart
	resFunc context.CancelFunc

	// resMu is the mutex that protects the res chan
	resMu sync.Mutex
}

// SignalRestart 'signals' to the Client that it should restart, meaning: reconnect to the server with the cached DNS
// This SHOULD be called when the VPN client switches networks such that you do not have to wait for a long
// 60 second timeout
func (c *Client) SignalRestart() error {
	c.resMu.Lock()
	defer c.resMu.Unlock()
	if c.resFunc == nil {
		return errors.New("no restarting function available")
	}

	c.resFunc()
	return nil
}

// configureSocket creates a TCP dial with fwmark/SO_MARK set
// it also calls the GotClientFD updater
func (c *Client) configureSocket(pips []string) net.Dialer {
	d := net.Dialer{
		Control: func(_, _ string, conn syscall.RawConn) error {
			var seterr error
			err := conn.Control(func(fd uintptr) {
				log.Log("Setting up socket...")
				defer log.Log("Done setting up socket...")
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
	first := true
	for {
		rctx, cancel := context.WithCancel(ctx)
		c.resMu.Lock()
		c.resFunc = cancel
		c.resMu.Unlock()
		err := c.tryTunnel(rctx, peer, pips, first)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-rctx.Done():
			continue
		case <-time.After(2 * time.Second):
		}
		if err != nil {
			log.Logf("Retrying as client exited with error: %v", err)
		} else {
			log.Logf("Retrying as client exited cleanly but context is not canceled yet")
		}
		first = false
	}
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
		return nil, err
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

// tryTunnel tries to tunnel the connection by connecting to HTTP peer `to` with IPs `pips`
// the boolean `first` is set if it's the first connect to the server
func (c *Client) tryTunnel(ctx context.Context, peer string, pips []string, first bool) (err error) {
	log.Log("Connecting to HTTP server...")
	u, err := url.Parse(peer)
	if err != nil {
		return err
	}

	peerhost := u.Host

	// set fwmark on the socket
	dialer := c.configureSocket(pips)
	if c.httpc == nil {
		c.httpc = &http.Client{}
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, addr string) (conn net.Conn, err error) {
			return c.dialContext(ctx, dialer, network, addr, peerhost, pips)
		},
	}
	c.httpc.Transport = transport

	req, err := http.NewRequestWithContext(ctx, "GET", peer, nil)
	if err != nil {
		return err
	}

	// upgrade the connection to wireguard
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", UpgradeProto)

	log.Log("Sending HTTP Upgrade...")

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
		return fmt.Errorf("status is not switching protocols, got: '%v'", resp.StatusCode)
	}

	if resp.Header.Get("Connection") != "Upgrade" {
		return fmt.Errorf("the 'Connection' header is not 'Upgrade', got: '%v'", resp.Header.Get("Connection"))
	}

	if resp.Header.Get("Upgrade") != UpgradeProto {
		return fmt.Errorf("upgrade header is not '%v', got: '%v'", UpgradeProto, resp.Header.Get("Upgrade"))
	}

	rwc, ok := rb.(io.ReadWriteCloser)
	if !ok {
		return fmt.Errorf("response body is not of type io.ReadWriteCloser: %T", rb)
	}
	log.Log("Connected to HTTP server")

	if c.Ready != nil && first {
		c.Ready()
	}

	udpaddr, err := net.ResolveUDPAddr("udp", c.Listen)
	if err != nil {
		return err
	}
	log.Log("Waiting for first UDP packet...")
	wgaddr, packet, err := inferUDPAddr(ctx, udpaddr)
	if err != nil {
		return err
	}
	log.Logf("First UDP packet received with address: %s", wgaddr.String())
	wgconn, err := net.DialUDP("udp", udpaddr, wgaddr)
	if err != nil {
		return err
	}
	defer wgconn.Close()
	log.Log("Client is ready for converting UDP<->HTTP")

	// create a buffered read writer
	rw := bufio.NewReadWriter(bufio.NewReader(rwc), bufio.NewWriter(rwc))

	// first forward the outstanding packet
	err = writeTCP(rw.Writer, packet, len(packet)-hdrLength)
	if err != nil {
		log.Logf("Failed forwarding first outstanding packet: %v", err)
	}

	return tunnel(ctx, wgconn, rw)
}
