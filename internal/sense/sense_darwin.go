//go:build darwin

package sense

import (
	"time"

	"github.com/KilianSen/used/internal/macos"
)

// macOS: everything goes through CoreGraphics / IOKit / CoreFoundation via
// purego, so the binary stays cgo-free and needs no permission prompts.

const (
	cgHIDSystemState = 1          // kCGEventSourceStateHIDSystemState: hardware events only
	cgAnyInputEvent  = 0xFFFFFFFF // kCGAnyInputEventType
)

// Assertion types that mean "someone is looking at the screen".
// UserIsActive is deliberately absent: any app can declare it.
var displayAssertions = map[string]bool{
	"PreventUserIdleDisplaySleep": true,
	"NoDisplaySleepAssertion":     true,
}

type darwinProbe struct {
	secondsSince   func(state int32, event uint32) float64
	sessionDict    func() uintptr
	copyAssertions func(out *uintptr) int32
	audio          *coreAudio

	keyLocked, keyOnConsole, keyType, keyName uintptr
}

func newProbe(Options) (Probe, error) {
	if err := macos.Load(); err != nil {
		return nil, err
	}
	p := &darwinProbe{audio: newCoreAudio()}
	macos.Func(&p.secondsSince, macos.CoreGraphics, "CGEventSourceSecondsSinceLastEventType")
	macos.Func(&p.sessionDict, macos.CoreGraphics, "CGSessionCopyCurrentDictionary")
	macos.Func(&p.copyAssertions, macos.IOKit, "IOPMCopyAssertionsByProcess")

	p.keyLocked = macos.Str("CGSSessionScreenIsLocked")
	p.keyOnConsole = macos.Str("kCGSSessionOnConsoleKey")
	p.keyType = macos.Str("AssertType")
	p.keyName = macos.Str("AssertName")
	return p, nil
}

func (p *darwinProbe) Read() (Reading, error) {
	r := Reading{
		At:     time.Now(),
		Idle:   time.Duration(p.secondsSince(cgHIDSystemState, cgAnyInputEvent) * float64(time.Second)),
		Source: SourceHID,
		Locked: p.locked(),
	}
	r.Evidence = append(p.assertions(), p.audio.evidence()...)
	return r, nil
}

func (p *darwinProbe) Close() error { return nil }

// locked is true when the screen is locked or this session is not the one on
// the console (fast user switching). No window-server session → not locked.
func (p *darwinProbe) locked() bool {
	d := p.sessionDict()
	if d == 0 {
		return false
	}
	defer macos.Release(d)
	if macos.Bool(macos.Get(d, p.keyLocked)) {
		return true
	}
	onConsole := macos.Get(d, p.keyOnConsole)
	return onConsole != 0 && !macos.Bool(onConsole)
}

// assertions lists processes holding a display-sleep assertion (video
// playback, presentations, calls), keyed by PID.
func (p *darwinProbe) assertions() []Evidence {
	var byPID uintptr
	if p.copyAssertions(&byPID) != 0 || byPID == 0 {
		return nil
	}
	defer macos.Release(byPID)

	var out []Evidence
	macos.Each(byPID, func(k, list uintptr) {
		for i := range macos.Len(list) {
			a := macos.At(list, i)
			if displayAssertions[macos.String(macos.Get(a, p.keyType))] {
				out = append(out, Evidence{Kind: KindAssertion, PID: int(macos.Int(k)), Detail: macos.String(macos.Get(a, p.keyName))})
			}
		}
	})
	return out
}
