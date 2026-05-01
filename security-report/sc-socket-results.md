# sc-socket Results

## Target: DFMT Unix Socket Server

## Summary

**All 8 security checks PASS.**

| Check | Status |
|---|---|
| Socket mode 0700 | PASS — `socket.go:60` `os.Chmod(s.path, 0700)` |
| TOCTOU race fixed (umask) | PASS — `socket_umask_unix.go` sets umask 077 before listen |
| Per-conn idle read deadline | PASS — `socket.go:119-121` `SetReadDeadline(60s)` |
| Panic recovery | PASS — `socket.go:105-110` defer recover + always close conn |
| Socket removed on shutdown | PASS — `socket.go:247` `os.Remove(s.path)` |
| Parent dir mode 0700 | PASS — `socket.go:43` `MkdirAll(dir, 0700)` |
| No CPU spin on close | PASS — `socket.go:78-98` checks `s.running` after Accept errors |
| No fd leaks on errors | PASS — `defer conn.Close()` always runs |

### Key Implementation Details

**TOCTOU fix** (`socket_umask_unix.go`):
```go
func listenUnixSocket(path string) (net.Listener, error) {
    old := syscall.Umask(0o077)
    defer syscall.Umask(old)
    return net.Listen("unix", path)
}
```
Combined with `os.Chmod(s.path, 0700)` in `socket.go:60`, the socket never exists briefly with group/other access.

**Per-connection idle timeout** (`socket.go:104-142`):
```go
func (s *SocketServer) handleConn(conn net.Conn) {
    defer func() {
        if r := recover(); r != nil { ... }
        _ = conn.Close()
    }()
    for {
        if socketReadIdleTimeout > 0 {
            _ = conn.SetReadDeadline(time.Now().Add(socketReadIdleTimeout))
        }
        req, err := s.codec.ReadRequest()
```

**Windows stub** (`socket_umask_windows.go`) — no-op stub (no umask on Windows). `os.Chmod` on line 60 of `socket.go` provides the permission fix on Windows.

## Conclusion

Socket security is correctly implemented. All findings from prior scans (V-3 TOCTOU, R16-2 idle timeout) are verified fixed. No issues found.