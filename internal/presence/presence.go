// Package presence turns sense readings into a machine state:
// active, present or unused.
//
// The core invariant is the latch: only trusted input can bring a machine
// out of `unused`. Evidence (fullscreen apps, display assertions, …) can keep
// a session `present` after real input, but never start one — so a workload
// scheduled onto an idle machine cannot make it look used.
package presence

import (
	"fmt"
	"slices"
	"time"

	"github.com/KilianSen/used/internal/sense"
)

type State string

const (
	Active  State = "active"
	Present State = "present"
	Unused  State = "unused"
)

// Config holds the timers and evidence weights. See DESIGN.md §2.5.
type Config struct {
	IdleAfter        time.Duration      // active → present after this long without input
	UnusedAfter      time.Duration      // present without evidence → unused
	PresentMax       time.Duration      // evidence alone can't hold present longer than this
	EvidenceHold     time.Duration      // evidence counts this long after last seen (debounce)
	LockIsUnused     bool               // lock / session switch → unused immediately
	CountRemoteInput bool               // treat RDP & co. as a local human
	Weights          map[string]float64 // evidence kind → weight; 0 or missing = ignored
	Unattributed     float64            // multiplier for evidence without a PID; 0 = ignored
}

// Override is a manual hold (force present) or release (force unused).
type Override struct {
	Mode   string     `json:"mode"` // none | hold | release
	Until  *time.Time `json:"until,omitempty"`
	Sticky bool       `json:"sticky,omitempty"` // release survives new input
	set    time.Time
}

// Status is the machine's state plus why it is in it.
type Status struct {
	State       State            `json:"state"`
	Previous    State            `json:"previous,omitempty"`
	Since       time.Time        `json:"since"`
	IdleSeconds int64            `json:"idle_seconds"`
	InputSource string           `json:"input_source"`
	Locked      bool             `json:"locked"`
	Remote      bool             `json:"remote"`
	Confidence  float64          `json:"confidence"`
	Reasons     []string         `json:"reasons"`
	Override    Override         `json:"override"`
	Evidence    []sense.Evidence `json:"evidence,omitempty"`
	Trace       []string         `json:"trace,omitempty"` // decision steps, for `used explain`
}

// How much we trust each input source.
var sourceTrust = map[string]float64{
	sense.SourceHID:        1.0,
	sense.SourceCompositor: 0.9,
	sense.SourceX11:        0.8,
	sense.SourceOS:         0.8,
	sense.SourceLogind:     0.5,
	sense.SourceNone:       0.3,
}

type Machine struct {
	cfg        Config
	st         Status
	unusedAt   time.Time // last time we entered unused; input must be newer to latch
	evidenceAt time.Time
	evidence   []sense.Evidence
	weight     float64
	ov         Override
}

func New(cfg Config) *Machine { return &Machine{cfg: cfg, ov: Override{Mode: "none"}} }

// SetOverride sets hold/release ("none" clears). A zero `for` lasts until cleared.
func (m *Machine) SetOverride(mode string, now time.Time, d time.Duration, sticky bool) error {
	switch mode {
	case "none", "hold", "release":
	default:
		return fmt.Errorf("unknown override %q", mode)
	}
	m.ov = Override{Mode: mode, Sticky: sticky, set: now}
	if d > 0 {
		until := now.Add(d)
		m.ov.Until = &until
	}
	return nil
}

func (m *Machine) Status() Status { return m.st }

// Update feeds one reading and returns the new status. changed reports a
// state transition.
func (m *Machine) Update(r sense.Reading) (s Status, changed bool) {
	var trace []string
	note := func(f string, a ...any) { trace = append(trace, fmt.Sprintf(f, a...)) }

	if ev, w := m.weigh(r.Evidence); len(ev) > 0 {
		m.evidenceAt, m.evidence, m.weight = r.At, ev, w
	}
	held := !m.evidenceAt.IsZero() && r.At.Sub(m.evidenceAt) <= m.cfg.EvidenceHold
	if !held {
		m.evidence, m.weight = nil, 0
	}

	lastInput := r.At.Add(-r.Idle)
	usable := r.Source != sense.SourceNone && (!r.Remote || m.cfg.CountRemoteInput)
	latched := usable && lastInput.After(m.unusedAt)
	note("input: %s, idle %s, remote=%t → usable=%t", r.Source, r.Idle.Round(time.Second), r.Remote, usable)
	note("latch: last input %s after last unused %s → %t", stamp(lastInput), stamp(m.unusedAt), latched)
	note("evidence: %d kept (weight %.2f), held=%t", len(m.evidence), m.weight, held)

	m.expireOverride(r.At, lastInput)
	trust := sourceTrust[r.Source]

	var state State
	var reasons []string
	conf := trust
	switch {
	case m.ov.Mode == "hold":
		state, reasons, conf = Present, []string{"override:hold"}, 1
	case m.ov.Mode == "release":
		state, reasons, conf = Unused, []string{"override:release"}, 1
	case r.Locked && m.cfg.LockIsUnused:
		state, reasons, conf = Unused, []string{"locked"}, 1
	case !usable && r.Remote:
		state, reasons = Unused, []string{"remote_session"}
	case !usable:
		state, reasons = Unused, []string{"no_input_source"}
	case !latched:
		state, reasons = Unused, []string{"no_input_since_unused"}
	case r.Idle < m.cfg.IdleAfter:
		state, reasons = Active, []string{"input"}
	case r.Idle < m.cfg.UnusedAfter:
		state, reasons = Present, []string{"recent_input"}
	case held && r.Idle < m.cfg.PresentMax:
		state, reasons, conf = Present, kinds(m.evidence), trust*m.weight
	case held:
		state, reasons = Unused, []string{"present_max"}
	default:
		state, reasons = Unused, []string{"idle"}
	}
	note("→ %s (%v)", state, reasons)

	prev := m.st.State
	if changed = state != prev; changed {
		m.st.Previous, m.st.Since = prev, r.At
		if state == Unused {
			m.unusedAt = r.At
		}
	}
	m.st.State = state
	m.st.IdleSeconds = int64(r.Idle / time.Second)
	m.st.InputSource = r.Source
	m.st.Locked, m.st.Remote = r.Locked, r.Remote
	m.st.Confidence = round2(conf)
	m.st.Reasons = reasons
	m.st.Override = m.ov
	m.st.Evidence = m.evidence
	m.st.Trace = trace
	return m.st, changed
}

// weigh drops evidence the config doesn't trust and returns the strongest weight.
func (m *Machine) weigh(in []sense.Evidence) (kept []sense.Evidence, best float64) {
	for _, e := range in {
		w := m.cfg.Weights[e.Kind]
		if e.PID == 0 {
			w *= m.cfg.Unattributed
		}
		if w > 0 {
			kept = append(kept, e)
			best = max(best, w)
		}
	}
	return kept, best
}

func (m *Machine) expireOverride(now, lastInput time.Time) {
	switch {
	case m.ov.Mode == "none":
		return
	case m.ov.Until != nil && now.After(*m.ov.Until):
	case m.ov.Mode == "release" && !m.ov.Sticky && lastInput.After(m.ov.set):
	default:
		return
	}
	m.ov = Override{Mode: "none"}
}

func kinds(ev []sense.Evidence) []string {
	var out []string
	for _, e := range ev {
		if !slices.Contains(out, e.Kind) {
			out = append(out, e.Kind)
		}
	}
	return out
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format("15:04:05")
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
