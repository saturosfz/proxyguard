package proxyguard

import (
	"net"
	"syscall"
)

// markedDial creates a TCP dial with fwmark/SO_MARK set
func markedDial(mark int, sport int) net.Dialer {
	d := net.Dialer{
		Control: func(network, address string, conn syscall.RawConn) error {
			var seterr error
			err := conn.Control(func(fd uintptr) {
				seterr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
			})
			if err != nil {
				return err
			}
			return seterr
		},
		LocalAddr: &net.TCPAddr{
			Port: sport,
		},
	}
	return d
}
