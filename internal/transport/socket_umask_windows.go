//go:build windows

package transport

import "net"

// ListenUnixSocket binds a Unix-domain socket at path. On Windows there is
// no umask, so this is a thin pass-through to net.Listen — kept exported
// for cross-platform call sites that don't want a build-tagged switch.
func ListenUnixSocket(path string) (net.Listener, error) {
	return net.Listen("unix", path)
}

func listenUnixSocket(path string) (net.Listener, error) {
	return ListenUnixSocket(path)
}
