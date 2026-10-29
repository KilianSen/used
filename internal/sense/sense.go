// Package sense reads raw presence signals from the OS: how long since the
// last trusted input, whether the session is locked, and supporting evidence
// (fullscreen apps, display-sleep assertions, …) attributed to a PID.
//
// It only observes. Interpreting the signals is the job of package presence.
package sense

import "time"

// Input sources, from most to least trustworthy.
const (
	SourceHID        = "hid"        // physical devices only; synthetic input filtered
	SourceCompositor = "compositor" // display server idle counter
	SourceX11        = "x11"        // X11 screensaver ext; counts XTEST input
	SourceOS         = "os_idle"    // OS idle counter; counts injected input
	SourceLogind     = "logind"     // coarse IdleHint from the session manager
	SourceNone       = "none"       // no usable input source
)

// Evidence kinds.
const (
	KindFullscreen = "fullscreen" // fullscreen / exclusive 3D / presentation
	KindAssertion  = "assertion"  // app keeps the display awake (video, call, …)
	KindAudio      = "audio"
	KindCamera     = "camera_mic"
)

// Evidence is a sign that someone may still be using the machine without
// touching it. PID 0 means the OS could not tell us who caused it.
type Evidence struct {
	Kind   string `json:"kind"`
	PID    int    `json:"pid,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Reading is one observation of the local session.
type Reading struct {
	At       time.Time
	Idle     time.Duration // time since last trusted input
	Source   string        // one of the Source* constants
	Locked   bool          // locked, switched away, or otherwise not at the console
	Remote   bool          // session is driven remotely (RDP, …)
	Evidence []Evidence
}

// Options tune what a probe is allowed to use.
type Options struct {
	// RawInput lets the Linux probe read /dev/input directly (needs the
	// `input` group) to get hardware-only idle time.
	RawInput bool
}

// Probe produces readings. Implementations are per OS.
type Probe interface {
	Read() (Reading, error)
	Close() error
}

// New returns the probe for the current OS.
func New(o Options) (Probe, error) { return newProbe(o) }
