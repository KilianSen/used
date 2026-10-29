package presence

import (
	"testing"
	"time"

	"github.com/KilianSen/used/internal/sense"
)

var cfg = Config{
	IdleAfter: 2 * time.Minute, UnusedAfter: 10 * time.Minute, PresentMax: 45 * time.Minute,
	EvidenceHold: 30 * time.Second, LockIsUnused: true,
	Weights: map[string]float64{sense.KindFullscreen: 1, sense.KindAssertion: 0.6},
}

var game = []sense.Evidence{{Kind: sense.KindFullscreen, PID: 42}}

// step is one reading `at` minutes into the scenario, `idle` minutes since input.
type step struct {
	at, idle float64
	locked   bool
	ev       []sense.Evidence
	want     State
}

func TestScenarios(t *testing.T) {
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	min := func(f float64) time.Duration { return time.Duration(f * float64(time.Minute)) }

	cases := map[string][]step{
		"game open, user on a break → present": {
			{at: 0, idle: 0, ev: game, want: Active},
			{at: 15, idle: 15, ev: game, want: Present},
		},
		"user left the office → unused": {
			{at: 0, idle: 0, want: Active},
			{at: 5, idle: 5, want: Present},
			{at: 11, idle: 11, want: Unused},
		},
		"game left open overnight → unused after present_max": {
			{at: 0, idle: 0, ev: game, want: Active},
			{at: 30, idle: 30, ev: game, want: Present},
			{at: 50, idle: 50, ev: game, want: Unused},
		},
		"scheduled fullscreen job on an unused machine stays unused": {
			{at: 0, idle: 60, want: Unused},
			{at: 1, idle: 61, ev: game, want: Unused},
			{at: 2, idle: 62, ev: game, want: Unused},
		},
		"lock is immediate, unlock needs fresh input": {
			{at: 0, idle: 0, want: Active},
			{at: 1, idle: 1, locked: true, want: Unused},
			{at: 2, idle: 2, want: Unused}, // input predates the lock
			{at: 3, idle: 0, want: Active},
		},
		"evidence flicker within hold keeps present": {
			{at: 0, idle: 0, ev: game, want: Active},
			{at: 12, idle: 12, ev: game, want: Present},
			{at: 12.25, idle: 12.25, want: Present},
			{at: 13, idle: 13, want: Unused},
		},
	}

	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			m := New(cfg)
			for i, s := range steps {
				got, _ := m.Update(sense.Reading{
					At: t0.Add(min(s.at)), Idle: min(s.idle), Source: sense.SourceHID,
					Locked: s.locked, Evidence: s.ev,
				})
				if got.State != s.want {
					t.Fatalf("step %d: got %s %v, want %s\n%v", i, got.State, got.Reasons, s.want, got.Trace)
				}
			}
		})
	}
}

func TestOverrides(t *testing.T) {
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	m := New(cfg)
	read := func(at, idle time.Duration) State {
		s, _ := m.Update(sense.Reading{At: t0.Add(at), Idle: idle, Source: sense.SourceHID})
		return s.State
	}

	_ = m.SetOverride("hold", t0, time.Hour, false)
	if got := read(30*time.Minute, 30*time.Minute); got != Present {
		t.Fatalf("hold: got %s", got)
	}
	if got := read(61*time.Minute, 61*time.Minute); got != Unused {
		t.Fatalf("hold expired: got %s", got)
	}

	_ = m.SetOverride("release", t0.Add(62*time.Minute), 0, false)
	if got := read(62*time.Minute, 0); got != Unused {
		t.Fatalf("release: got %s", got)
	}
	if got := read(63*time.Minute, 0); got != Active {
		t.Fatalf("input after release should end it: got %s", got)
	}
}
