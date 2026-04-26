//go:build !windows

package transport

import (
	"net"
	"syscall"
)

func listenUnixSocket(path string) (net.Listener, error) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	return net.Listen("unix", path)
}
