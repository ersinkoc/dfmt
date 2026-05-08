//go:build !windows

package transport

import (
	"errors"
	"syscall"
)

// isPortUnavailable reports whether a net.Listen error means the requested
// port cannot be bound right now and an ephemeral-port fallback is the
// right move.
//
// On Unix the two relevant errnos are EADDRINUSE (port already bound by
// another listener) and EACCES (binding to a privileged port < 1024
// without CAP_NET_BIND_SERVICE). The latter is rare in DFMT because the
// default config picks ports >= 1024, but operators occasionally pick
// 80 / 443 for proxy convenience and we want the daemon to come up on
// an ephemeral port rather than refuse to start.
func isPortUnavailable(err error) bool {
	return errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EADDRINUSE)
}
