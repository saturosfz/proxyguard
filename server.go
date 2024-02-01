package proxyguard

import (
	"context"
	"net"
	"net/http"
	"time"

	"nhooyr.io/websocket"
)

type wsServer struct {
	wgaddr *net.UDPAddr
}

func (s wsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	wgconn, err := net.DialUDP("udp", nil, s.wgaddr)
	if err != nil {
		log.Logf("Failed dialing WireGuard: %v", err)
		return
	}
	wsc, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Logf("Failed accepting client: %v", err)
		return
	}
	defer wsc.Close(websocket.StatusNormalClosure, "")
	tunnel(r.Context(), wgconn, wsc)
}

// Server creates a server that forwards TCP to UDP
// wgp is the WireGuard port
// tcpp is the TCP listening port
// to is the IP:PORT string
func Server(ctx context.Context, listen string, to string) error {
	wgaddr, err := net.ResolveUDPAddr("udp", to)
	if err != nil {
		return err
	}
	tcpaddr, err := net.ResolveTCPAddr("tcp", listen)
	if err != nil {
		return err
	}
	tcpconn, err := net.ListenTCP("tcp", tcpaddr)
	if err != nil {
		return err
	}
	s := &http.Server{
		Handler:      wsServer{wgaddr: wgaddr},
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	errc := make(chan error, 1)
	go func() {
		errc <- s.Serve(tcpconn)
	}()
	defer s.Shutdown(ctx)

	for {
		select {
		case err := <-errc:
			log.Logf("failed to serve: %v", err)
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
