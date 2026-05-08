//go:build windows

package transport

import (
	"errors"
	"syscall"
)

// Winsock error numbers that aren't exported as named constants in Go's
// syscall package on Windows. WSAEACCES is exported; WSAEADDRINUSE is
// not. Defining them locally keeps the bind-fallback decision readable
// without introducing a golang.org/x/sys dependency (the project's
// dep policy permits x/sys, but a single errno doesn't justify it).
const (
	wsaeAddrInUse syscall.Errno = 10048 // WSAEADDRINUSE
)

// isPortUnavailable reports whether a net.Listen error means the requested
// port cannot be bound right now and an ephemeral-port fallback is the
// right move.
//
// On Windows two errno values dominate this case:
//
//   - WSAEACCES (10013): the port is inside an OS-administered exclusion
//     range (`netsh int ipv4 show excludedportrange protocol=tcp`). Hyper-V,
//     WSL2 and Docker Desktop each carve out 100-port blocks at boot time,
//     and the blocks shift between reboots — a port that worked yesterday
//     can be reserved today. The error text is the famously unhelpful
//     "An attempt was made to access a socket in a way forbidden by its
//     access permissions."
//   - WSAEADDRINUSE (10048): the port is held by another live listener.
//
// The Unix EACCES / EADDRINUSE constants are also checked because Go's
// net package occasionally surfaces them on Windows when the syscall
// path is normalized through the os.SyscallError chain.
func isPortUnavailable(err error) bool {
	return errors.Is(err, syscall.WSAEACCES) ||
		errors.Is(err, wsaeAddrInUse) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EADDRINUSE)
}
