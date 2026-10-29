package daemon

import "golang.org/x/sys/unix"

func peerFD(fd int) int {
	pid, _ := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	return pid
}
