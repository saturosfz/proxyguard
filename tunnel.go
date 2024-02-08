package proxyguard

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"
)

// bufSize is the total length that we receive at once
// 2^16
const bufSize = 2 << 15

// hdrLength is the length of our own crafted header
// This header contains the length of a UDP packet
const hdrLength = 2

// writeUDPChunks writes UDP packets from buffer to the connection
// As our packets are prefixed with a 2 byte UDP size header,
// we loop through the buffer up until nothing is left to write or up until we find a non-complete packet
func writeUDPChunks(conn net.Conn, buf []byte) int {
	idx := 0
	for {
		// get the header length index
		hdre := idx + hdrLength
		if len(buf) < hdre {
			return idx
		}
		hdr := buf[idx:hdre]
		// get the lenth of the datagram from the header we made
		n := binary.BigEndian.Uint16(hdr)

		// the datagram ends after the header + size
		dge := hdre + int(n)
		if len(buf) < dge {
			return idx
		}
		datagram := buf[hdre:dge]
		// write and check if the write length is not equal
		_, err := conn.Write(datagram)
		if err != nil {
			return idx
		}
		idx = dge
	}
}

// writeTCP writes a buffer to the connection
// This buffer is prefixed with a 2 byte length specified with n
func writeTCP(w *bufio.Writer, buf []byte, n int) error {
	// Put the header length at the front
	binary.BigEndian.PutUint16(buf[:hdrLength], uint16(n))
	// store the length and packet itself
	_, werr := w.Write(buf)
	w.Flush()
	return werr
}

// tcpToUDP reads from the TCP reader r and writes packets to the udpc connection
// The incoming TCP packets are encapsulated UDP packets with a 2 byte length prefix
func tcpToUDP(r *bufio.Reader, udpc *net.UDPConn) error {
	var bufr [bufSize]byte
	todo := 0
	for {
		n, rerr := r.Read(bufr[todo:])
		if n > 0 {
			todo += n
			done := writeUDPChunks(udpc, bufr[:todo])

			// There is still data left to be written
			// Copy to front
			if todo > done {
				diff := todo - done
				copy(bufr[:diff], bufr[done:todo])
			}
			todo -= done
		}
		if rerr != nil {
			return rerr
		}
	}
}

// udpToWS reads from the UDP connection udpc and writes packets to the wsc connection
// The incoming UDP packets are encapsulated inside TCP with a 2 byte length prefix
func udpToTCP(udpc *net.UDPConn, w *bufio.Writer) error {
	var bufs [bufSize]byte
	for {
		n, _, rerr := udpc.ReadFromUDP(bufs[2:])
		if n > 0 {
			werr := writeTCP(w, bufs[:n+2], n)
			if werr != nil {
				return werr
			}
		}
		if rerr != nil {
			return rerr
		}
	}
}

// inferUDPAddr gets the UDP address from the first packet that is sent to the proxy
func inferUDPAddr(ctx context.Context, laddr *net.UDPAddr) (*net.UDPAddr, []byte, error) {
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	cancel := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				_ = conn.SetReadDeadline(time.Now())
			case <-cancel:
				return
			}
		}
	}()
	defer close(cancel)
	var tempbuf [bufSize]byte
	n, addr, err := conn.ReadFromUDP(tempbuf[hdrLength:])
	if err != nil {
		return nil, nil, err
	}
	if addr != nil {
		return addr, tempbuf[:n+hdrLength], nil
	}
	return nil, nil, errors.New("could not infer port because address was nil")
}

func tunnel(ctx context.Context, udpc *net.UDPConn, rw *bufio.ReadWriter) {
	cancel := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				_ = udpc.SetDeadline(time.Now())
			case <-cancel:
				return
			}
		}
	}()
	defer close(cancel)
	wg := sync.WaitGroup{}
	wg.Add(1)
	// read from udp and write to ws socket
	go func() {
		defer wg.Done()
		err := udpToTCP(udpc, rw.Writer)
		if err != nil {
			log.Logf("UDP -> TCP completed with error: %v", err)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := tcpToUDP(rw.Reader, udpc)
		if err != nil {
			log.Logf("TCP -> UDP completed with error: %v", err)
		}
	}()
	wg.Wait()
}
