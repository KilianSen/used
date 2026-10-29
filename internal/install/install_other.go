//go:build !darwin && !linux && !windows

package install

import (
	"fmt"
	"runtime"
)

func install(string) (string, error) {
	return "", fmt.Errorf("install is not supported on %s", runtime.GOOS)
}
func uninstall() (string, error) {
	return "", fmt.Errorf("install is not supported on %s", runtime.GOOS)
}
