//go:build !windows

package transport

import (
	"net"
	"syscall"
)

// ListenUnixSocket binds a Unix-domain socket at path with a 0o077 umask
// applied for the duration of the bind so the socket file is never
// world-readable in the window between bind(2) and any subsequent chmod.
// Closes F-05 (daemon Unix socket bound without umask wrapper).
func ListenUnixSocket(path string) (net.Listener, error) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	return net.Listen("unix", path)
}

// listenUnixSocket is an internal alias kept for the existing socket.go
// callsite; new callers should use the exported ListenUnixSocket.
func listenUnixSocket(path string) (net.Listener, error) {
	return ListenUnixSocket(path)
}
