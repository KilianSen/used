// Package daemon runs used: it polls the probe, keeps the presence state,
// tracks workloads, samples metrics and publishes events to webhooks and
// the local API.
package daemon

import (
	"sync"
	"time"

	"github.com/KilianSen/used/internal/presence"
	"github.com/KilianSen/used/internal/sense"
	"github.com/KilianSen/used/internal/workload"
)

// Filtered is evidence dropped because a workload caused it.
type Filtered struct {
	sense.Evidence
	Tag string `json:"tag"`
}

// Snapshot is the result of one tick.
type Snapshot struct {
	Status   presence.Status  `json:"status"`
	Filtered []Filtered       `json:"filtered,omitempty"`
	Ended    []workload.Lease `json:"-"`
	Changed  bool             `json:"-"`
}

// Engine is one probe → filter → state machine pipeline. The daemon ticks it
// every second; `used status` without a daemon ticks it once.
type Engine struct {
	mu       sync.Mutex
	probe    sense.Probe
	machine  *presence.Machine
	reg      *workload.Registry
	appNames bool
}

func NewEngine(p sense.Probe, m *presence.Machine, reg *workload.Registry, appNames bool) *Engine {
	return &Engine{probe: p, machine: m, reg: reg, appNames: appNames}
}

func (e *Engine) Tick() Snapshot {
	r, err := e.probe.Read()
	if err != nil {
		r = sense.Reading{At: time.Now(), Source: sense.SourceNone}
	}
	var snap Snapshot
	snap.Ended = e.reg.Prune(r.At, workload.Alive)

	procs := workload.NewProcs()
	kept := r.Evidence[:0]
	for _, ev := range r.Evidence {
		if !e.appNames && ev.Kind == sense.KindAssertion {
			ev.Detail = "" // app-supplied text, may name what's playing
		}
		if tag, ok := e.reg.Owner(ev.PID, procs); ok {
			snap.Filtered = append(snap.Filtered, Filtered{ev, tag})
			continue
		}
		kept = append(kept, ev)
	}
	r.Evidence = kept

	e.mu.Lock()
	snap.Status, snap.Changed = e.machine.Update(r)
	e.mu.Unlock()
	return snap
}

func (e *Engine) SetOverride(mode string, d time.Duration, sticky bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.machine.SetOverride(mode, time.Now(), d, sticky)
}
