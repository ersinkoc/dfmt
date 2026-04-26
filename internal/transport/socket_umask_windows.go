//go:build windows

package transport

import "net"

func listenUnixSocket(path string) (net.Listener, error) {
	return net.Listen("unix", path)
}
