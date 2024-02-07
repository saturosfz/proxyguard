package proxyguard

import (
	"context"
	"net"
	"net/http"

	"nhooyr.io/websocket"
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

	log.Log("Connecting to Websocket server...")
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
	opts := websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				Dial: dialer.Dial,
			},
		},
	}
	wsc, _, err := websocket.Dial(ctx, to, &opts)
	if err != nil {
		return err
	}
	defer wsc.Close(websocket.StatusNormalClosure, "")
	log.Log("Connected to Websocket server")

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
	log.Log("Client is ready for converting UDP<->WS")

	// first forward the outstanding packet
	err = writeWS(ctx, wsc, first, len(first)-hdrLength)
	if err != nil {
		log.Logf("Failed forwarding first outstanding packet: %v", err)
	}

	tunnel(ctx, wgconn, wsc)
	return nil
}
