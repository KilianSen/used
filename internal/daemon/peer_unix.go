//go:build linux || darwin

package daemon

import "net"

// connPID returns the PID of the process on the other end of a unix socket,
// as seen from our PID namespace (so a caller inside a container resolves to
// its host PID).
func connPID(c net.Conn) int {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) { pid = peerFD(int(fd)) })
	return pid
}
