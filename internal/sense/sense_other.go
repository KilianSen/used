//go:build !darwin && !linux && !windows

package sense

import (
	"fmt"
	"runtime"
)

func newProbe(Options) (Probe, error) {
	return nil, fmt.Errorf("presence detection is not supported on %s", runtime.GOOS)
}
