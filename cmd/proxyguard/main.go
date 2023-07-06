package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"sync"
)

// BufSize is the total length that we receive at once
// 2^16
const BufSize = 2 << 15

// HdrLength is the length of our own crafted header
// This header contains the length of a UDP packet
const HdrLength = 2

// writeUDPChunks writes UDP packets from buffer to the connection
// As our packets are prefixed with a 2 byte UDP size header,
// we loop through the buffer up until nothing is left to write or up until we find a non-complete packet
func writeUDPChunks(conn net.Conn, buf []byte) int {
	idx := 0
	for {
		// get the header length index
		hdre := idx + HdrLength
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
func writeTCP(conn net.Conn, buf []byte, n int) error {
	// Put the header length at the front
	binary.BigEndian.PutUint16(buf[:HdrLength], uint16(n))
	// store the length and packet itself
	_, werr := conn.Write(buf[:])
	return werr
}

// TCPToUDP reads from the TCP connection tcpc and writes packets to the udpc connection
// The incoming TCP packets are encapsulated UDP packets with a 2 byte length prefix
func TCPToUDP(tcpc *net.TCPConn, udpc *net.UDPConn) error {
	var bufr [BufSize]byte
	todo := 0
	for {
		n, rerr := tcpc.Read(bufr[todo:])
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

// TCPToUDP reads from the UDP connection udpc and writes packets to the tcpc connection
// The incoming UDP packets are encapsulated inside TCP with a 2 byte length prefix
func UDPToTCP(udpc *net.UDPConn, tcpc *net.TCPConn) error {
	var bufs [BufSize]byte
	for {
		n, _, rerr := udpc.ReadFromUDP(bufs[2:])
		if n > 0 {
			werr := writeTCP(tcpc, bufs[:n+2], n)
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
func inferUDPAddr(laddr *net.UDPAddr) (*net.UDPAddr, []byte, error) {
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()

	var tempbuf [BufSize]byte
	n, addr, err := conn.ReadFromUDP(tempbuf[HdrLength:])
	if addr != nil {
		return addr, tempbuf[:n+HdrLength], nil
	}
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, errors.New("could not infer port because address was nil")
}

// client creates a client that forwards UDP to TCP
// listen is the IP:PORT port
// to is the IP:PORT string for the TCP proxy on the other end
// fwmark is the mark to set on the TCP socket such that we do not get a routing loop
func client(listen string, to string, fwmark int) error {
	var conn net.Conn
	var derr error
	log.Println("Connecting to TCP server...")
	// set fwmark
	if fwmark != -1 {
		conn, derr = markedDial(fwmark, to)
	} else {
		conn, derr = net.Dial("tcp", to)
	}
	if derr != nil {
		return derr
	}
	defer conn.Close()
	tcpc, ok := conn.(*net.TCPConn)
	if !ok {
		return errors.New("connection is not a TCP connection")
	}
	log.Println("Connected to TCP server")

	udpaddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return err
	}
	log.Println("Waiting for first UDP packet...")
	wgaddr, first, err := inferUDPAddr(udpaddr)
	if err != nil {
		return err
	}
	log.Println("First UDP packet received with address:", wgaddr.String())
	wgconn, err := net.DialUDP("udp", udpaddr, wgaddr)
	if err != nil {
		return err
	}
	defer wgconn.Close()
	wg := sync.WaitGroup{}
	log.Println("Client is ready for converting UDP<->TCP")

	// first forward the outstanding packet
	writeTCP(tcpc, first[:], len(first)-HdrLength)

	wg.Add(1)
	// read from udp and write to tcp socket
	go func() {
		defer wg.Done()
		UDPToTCP(wgconn, tcpc)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		TCPToUDP(tcpc, wgconn)
	}()
	wg.Wait()
	return nil
}

// server creates a server that forwards TCP to UDP
// wgp is the WireGuard port
// tcpp is the TCP listening port
// to is the IP:PORT string
func server(listen string, to string) error {
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
	defer tcpconn.Close()
	log.Println("Proxy server is ready to receive clients...")
	// Begin accepting TCP connections
	for {

		conn, err := tcpconn.AcceptTCP()
		if err != nil {
			log.Println("Failed to accept client", err)
			continue
		}
		// We got a successful connection
		// Handle it in a goroutine so that we can continue listening
		go func(conn *net.TCPConn) {
			defer conn.Close()
			// Check if we can connect to WireGuard
			wgconn, err := net.DialUDP("udp", nil, wgaddr)
			if err != nil {
				log.Println("Failed to connect to wg", err)
				return
			}
			defer wgconn.Close()
			wg := sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				TCPToUDP(conn, wgconn)
			}()
			wg.Add(1)
			// handle outgoing
			go func() {
				defer wg.Done()
				UDPToTCP(wgconn, conn)
			}()
			wg.Wait()
		}(conn)
	}
}

func main() {
	fwmark := flag.Int("fwmark", -1, "[Client + Linux only] The fwmark/SO_MARK to use on the TCP client socket. -1 is disable.")
	listen := flag.String("listen", "", "The IP:PORT to listen for UDP (client) or TCP (server) traffic.")
	to := flag.String("to", "", "The IP:PORT to which to send the converted traffic to. For the client you would specify a TCP server, for the server the WireGuard endpoint. The WireGuard endpoint for the client is automatically inferred from the first UDP packet")
	ic := flag.Bool("client", false, "Indicates that this should function as a client, proxying UDP packets to a TCP server")
	is := flag.Bool("server", false, "Indicates that this should function as a server, proxying TCP packets to UDP")
	flag.Parse()
	// Both a client and server flag are not supplied
	if !*ic && !*is {
		fmt.Fprintln(os.Stderr, "Invalid invocation error: Please supply the --client or --server flag")
		flag.PrintDefaults()
		os.Exit(1)
	}
	// Both a client and server flag are supplied
	if *ic && *is {
		fmt.Fprintln(os.Stderr, "Invalid invocation error: Please supply only one --client or --server flag")
		flag.PrintDefaults()
		os.Exit(1)
	}
	// listen and to flags are also mandatory
	if *listen == "" {
		fmt.Fprintln(os.Stderr, "Invalid invaction error: Please supply the --listen flag")
		flag.PrintDefaults()
		os.Exit(1)
	}
	if *to == "" {
		fmt.Fprintln(os.Stderr, "Invalid invocation error: Please supply the --to flag")
		flag.PrintDefaults()
		os.Exit(1)
	}
	// Warn that the server does not use fwmark
	if *is && *fwmark != -1 {
		fmt.Fprintln(os.Stderr, "Invalid invocation warning: The --fwmark flag is a NO-OP for the server")
		*fwmark = -1
	}
	// fwmark flag is given for the client but we are not linux
	if *ic && *fwmark != -1 && runtime.GOOS != "linux" {
		fmt.Fprintln(os.Stderr, "Invalid invocation warning: The --fwmark flag is a NO-OP when you're not using Linux. We will ignore it...")
		*fwmark = -1
	}
	// We are a client
	if *ic {
		err := client(*listen, *to, *fwmark)
		if err != nil {
			log.Fatalf("error occurred when setting up a client: %v", err)
		}
		return
	}
	// We are a server
	err := server(*listen, *to)
	if err != nil {
		log.Fatalf("error occurred when setting up server side: %v", err)
	}
}
