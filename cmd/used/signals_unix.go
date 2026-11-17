//go:build !windows

package main

import (
	"os"
	"syscall"
)

var forwardSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2}
