// Package notify shows desktop notifications for events a person at the
// machine would care about: a workload started here, you came back to a
// machine with work running, an override changed. One small backend per OS
// implements show.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/KilianSen/used/internal/bus"
)

type Config struct {
	Enabled bool     `toml:"enabled"`
	Events  []string `toml:"events"` // empty = all that have a message
}

// Show displays one notification with the platform backend.
func Show(title, body string) error { return show(title, body) }

type Sink struct {
	cfg Config
	log *slog.Logger
}

func NewSink(cfg Config, log *slog.Logger) *Sink {
	return &Sink{cfg: cfg, log: log.With("sink", "notify")}
}

func (s *Sink) Run(ctx context.Context, events <-chan bus.Msg) {
	failed := false
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-events:
			if !ok {
				return
			}
			if !bus.Wants(s.cfg.Events, m.Type) {
				continue
			}
			title, body, ok := Message(m.Body)
			if !ok {
				continue
			}
			if err := show(title, body); err != nil && !failed {
				s.log.Warn("notification failed (further failures not logged)", "err", err)
				failed = true
			}
		}
	}
}

// event is the subset of the daemon payload that notifications read.
type event struct {
	Event    string `json:"event"`
	Presence struct {
		State    string `json:"state"`
		Previous string `json:"previous"`
		Override struct {
			Mode  string     `json:"mode"`
			Until *time.Time `json:"until"`
		} `json:"override"`
	} `json:"presence"`
	Workloads []struct {
		Tag string `json:"tag"`
	} `json:"workloads"`
	Lease *struct {
		Tag string `json:"tag"`
	} `json:"lease"`
}

// Message turns an event payload into a notification, or ok=false when the
// event isn't worth interrupting anyone for.
func Message(body []byte) (title, text string, ok bool) {
	var ev event
	if json.Unmarshal(body, &ev) != nil {
		return "", "", false
	}
	switch ev.Event {
	case "workload.started":
		if ev.Lease != nil {
			return "Workload started", tag(ev.Lease.Tag) + " is now running on this machine.", true
		}
	case "workload.ended":
		if ev.Lease != nil {
			return "Workload finished", tag(ev.Lease.Tag) + " is no longer running.", true
		}
	case "state.changed":
		// Only the moment someone returns to a machine that has work on it.
		if ev.Presence.State == "active" && ev.Presence.Previous == "unused" && len(ev.Workloads) > 0 {
			tags := make([]string, len(ev.Workloads))
			for i, w := range ev.Workloads {
				tags[i] = tag(w.Tag)
			}
			return "Welcome back", fmt.Sprintf("%d workload(s) running: %s. Your scheduler has been told you're here.",
				len(tags), strings.Join(tags, ", ")), true
		}
	case "override.changed":
		o := ev.Presence.Override
		until := ""
		if o.Until != nil {
			until = " until " + o.Until.Local().Format("15:04")
		}
		switch o.Mode {
		case "hold":
			return "Holding", "This machine is reported as in use" + until + ".", true
		case "release":
			return "Released", "This machine is reported as unused" + until + ".", true
		default:
			return "Automatic", "Presence detection is back in charge.", true
		}
	}
	return "", "", false
}

func tag(t string) string {
	if t == "" {
		return "A workload"
	}
	return t
}
