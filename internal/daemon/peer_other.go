//go:build !linux && !darwin

package daemon

import "net"

// connPID: Windows AF_UNIX sockets don't expose the peer, so clients send
// their PID explicitly.
func connPID(net.Conn) int { return 0 }
