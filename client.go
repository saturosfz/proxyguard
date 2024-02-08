package proxyguard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
)

// Client creates a client that forwards UDP to TCP
// listen is the IP:PORT port
// tcpsp is the TCP source port
// to is the IP:PORT string for the TCP proxy on the other end
// fwmark is the mark to set on the TCP socket such that we do not get a routing loop, use -1 to disable setting fwmark
func Client(ctx context.Context, listen string, tcpsp int, to string, fwmark int) (err error) {
	defer func() {
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		default:
			return
		}
	}()

	log.Log("Connecting to HTTP server...")
	if tcpsp == -1 {
		laddr, err := net.ResolveTCPAddr("tcp", listen)
		if err != nil {
			return err
		}
		tcpsp = laddr.Port
	}

	var dialer net.Dialer
	// set fwmark
	if fwmark != -1 {
		dialer = markedDial(fwmark, tcpsp)
	} else {
		dialer = net.Dialer{
			LocalAddr: &net.TCPAddr{
				Port: tcpsp,
			},
		}
	}
	c := &http.Client{
		Transport: &http.Transport{
			Dial: dialer.Dial,
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", to, nil)
	if err != nil {
		return err
	}

	// upgrade the connection to wireguard
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", UpgradeProto)

	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	// TODO: why does nhooyr.io/websocket set the body to nil and make a rb copy?
	// is this needed?
	rb := resp.Body
	resp.Body = nil

	// TODO: clean this up?
	cancel := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				rb.Close()
				return
			case <-cancel:
				rb.Close()
				return
			}
		}
	}()
	defer close(cancel)

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

	udpaddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return err
	}
	log.Log("Waiting for first UDP packet...")
	wgaddr, first, err := inferUDPAddr(ctx, udpaddr)
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
	err = writeTCP(rw.Writer, first, len(first)-hdrLength)
	if err != nil {
		log.Logf("Failed forwarding first outstanding packet: %v", err)
	}

	tunnel(ctx, wgconn, rw)
	return nil
}
