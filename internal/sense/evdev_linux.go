//go:build linux

package sense

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// evdev watches /dev/input/event* for physical key, button and pointer
// events. Virtual devices (uinput, i.e. software-injected input) and
// sensors that stream constantly (accelerometers) are skipped.

const evdevEventSize = 24 // struct input_event on 64-bit: timeval(16) + type(2) + code(2) + value(4)

type evdev struct{ last atomic.Int64 }

func startEvdev() (*evdev, error) {
	paths, _ := filepath.Glob("/dev/input/event*")
	e, opened := &evdev{}, 0
	for _, path := range paths {
		if skipDevice(filepath.Base(path)) {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		opened++
		go e.watch(f)
	}
	if opened == 0 {
		return nil, errors.New("no readable input devices (is the user in the `input` group?)")
	}
	return e, nil
}

func skipDevice(name string) bool {
	sys := "/sys/class/input/" + name + "/device"
	if real, err := filepath.EvalSymlinks(sys); err != nil || strings.Contains(real, "/devices/virtual/") {
		return true
	}
	label, _ := os.ReadFile(sys + "/name")
	return strings.Contains(strings.ToLower(string(label)), "accel")
}

func (e *evdev) watch(f *os.File) {
	defer f.Close()
	buf := make([]byte, evdevEventSize*64)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		for off := 0; off+evdevEventSize <= n; off += evdevEventSize {
			switch buf[off+16] { // event type (low byte, little-endian)
			case 1, 2, 3: // EV_KEY, EV_REL, EV_ABS
				e.last.Store(time.Now().UnixNano())
			}
		}
	}
}

func (e *evdev) seen() bool          { return e.last.Load() != 0 }
func (e *evdev) idle() time.Duration { return time.Since(time.Unix(0, e.last.Load())) }
