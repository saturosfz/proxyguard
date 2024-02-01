//go:build !linux

package proxyguard

import "net"

// fwmark/SO_MARK is not supported (unless Linux) so we crash here as this should not have been called anyways
// If more targets support this then the _linux.go file needs to be renamed to _other.go and set the proper build guards here and there
func markedDial(mark int, sport int) net.Dialer {
	panic("SO_MARK is not supported for this platform")
}
