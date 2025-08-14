//go:build !linux

package proxyguard

func socketReuseSport(_ int) error {
	panic("reusing a source port is not supported on this OS")
}

func socketFWMark(_ int, _ int) error {
	panic("setting fwmark is not supported on this OS")
}
