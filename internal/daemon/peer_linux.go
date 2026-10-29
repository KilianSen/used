package daemon

import "golang.org/x/sys/unix"

func peerFD(fd int) int {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0
	}
	return int(cred.Pid)
}
