package proxyguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type tunnelServer struct {
	wgaddr *net.UDPAddr
}

func (s tunnelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		log.Logf("Error accepting client with HTTP method: %v", r.Method)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusNotImplemented)
		return
	}

	if r.Header.Get("Connection") != "Upgrade" {
		err := fmt.Errorf("the 'Connection' header is not 'Upgrade', got: '%v'", r.Header.Get("Connection"))
		log.Logf("Error accepting client: %v", err)
		http.Error(w, err.Error(), http.StatusUpgradeRequired)
		return
	}

	if r.Header.Get("Upgrade") != UpgradeProto {
		err := fmt.Errorf("the 'Upgrade' header is not '%s', got: '%v'", UpgradeProto, r.Header.Get("Upgrade"))
		log.Logf("Error accepting client: %v", err)
		http.Error(w, err.Error(), http.StatusUpgradeRequired)
		return
	}

	// upgrade to wireguard protocol
	w.Header().Set("Upgrade", UpgradeProto)
	w.Header().Set("Connection", "Upgrade")

	hj, ok := w.(http.Hijacker)
	if !ok {
		err := errors.New("the HTTP response writer does not implement the hijacker interface")
		log.Logf("Error accepting client: %v", err)
		http.Error(w, http.StatusText(http.StatusNotImplemented), http.StatusNotImplemented)
		return
	}

	// switch protocol to wireguard
	w.WriteHeader(http.StatusSwitchingProtocols)

	// hijack the connection so that we get a TCP stream
	conn, brw, err := hj.Hijack()
	if err != nil {
		err = fmt.Errorf("hijacking connection failed: %w", err)
		log.Logf("Error accepting client: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	// we have hijacked the connection
	// the connection should be closed at the end
	// we use the brw as a buffer that we can read/write to
	defer conn.Close()

	// stolen from nhooyr.io/websocket
	// https://github.com/golang/go/issues/32314
	// TODO: is this really needed for us?
	b, _ := brw.Reader.Peek(brw.Reader.Buffered())
	brw.Reader.Reset(io.MultiReader(bytes.NewReader(b), conn))

	// connect to WireGuard
	wgconn, err := net.DialUDP("udp", nil, s.wgaddr)
	if err != nil {
		log.Logf("Failed dialing WireGuard: %v", err)
		return
	}

	// tunnel the traffic using the buffered connection
	tunnel(r.Context(), wgconn, brw)
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
		Handler:      tunnelServer{wgaddr: wgaddr},
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	errc := make(chan error, 1)
	go func() {
		errc <- s.Serve(tcpconn)
	}()
	defer func() {
		_ = s.Shutdown(ctx)
	}()

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
