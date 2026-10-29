package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/KilianSen/used/internal/workload"
)

// Explain is everything that went into the current decision.
type Explain struct {
	Snapshot
	Leases []workload.Lease `json:"leases"`
	Rules  []workload.Rule  `json:"rules"`
	Config any              `json:"config"`
}

// LeaseRequest registers a workload. PID 0 means "the caller": the daemon
// uses the socket peer's PID, which is correct even from inside a container.
type LeaseRequest struct {
	Tag  string            `json:"tag"`
	PID  int               `json:"pid"`
	TTL  string            `json:"ttl,omitempty"`
	Meta map[string]string `json:"meta,omitempty"`
}

type OverrideRequest struct {
	Mode   string `json:"mode"` // hold | release | none
	For    string `json:"for,omitempty"`
	Sticky bool   `json:"sticky,omitempty"`
}

func (d *Daemon) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, d.event("status", nil))
	})
	mux.HandleFunc("GET /v1/explain", func(w http.ResponseWriter, r *http.Request) {
		d.mu.RLock()
		snap := d.last
		d.mu.RUnlock()
		reply(w, http.StatusOK, Explain{Snapshot: snap, Leases: d.reg.Leases(), Rules: d.cfg.Ignore, Config: d.cfg.Presence})
	})
	mux.HandleFunc("GET /v1/events", d.stream)
	mux.HandleFunc("POST /v1/shutdown", func(w http.ResponseWriter, r *http.Request) {
		d.log.Info("shutdown requested")
		w.WriteHeader(http.StatusNoContent)
		go d.stop()
	})
	mux.HandleFunc("POST /v1/workloads", d.addLease)
	mux.HandleFunc("DELETE /v1/workloads/{id}", func(w http.ResponseWriter, r *http.Request) {
		l, ok := d.reg.Remove(r.PathValue("id"))
		if !ok {
			fail(w, http.StatusNotFound, "no such workload")
			return
		}
		d.publish("workload.ended", &l)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/override", func(w http.ResponseWriter, r *http.Request) {
		var req OverrideRequest
		dur, ok := decode(w, r, &req, func() string { return req.For })
		if !ok {
			return
		}
		if err := d.engine.SetOverride(req.Mode, dur, req.Sticky); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		d.step() // apply now, not on the next tick
		d.publish("override.changed", nil)
		reply(w, http.StatusOK, d.event("status", nil))
	})
	return mux
}

func (d *Daemon) addLease(w http.ResponseWriter, r *http.Request) {
	var req LeaseRequest
	ttl, ok := decode(w, r, &req, func() string { return req.TTL })
	if !ok {
		return
	}
	if req.PID == 0 {
		req.PID = peerPID(r.Context())
	}
	l, err := d.reg.Add(req.Tag, req.PID, ttl, req.Meta)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	d.log.Info("workload registered", "tag", l.Tag, "pid", l.PID, "expires", l.Expires.Format(time.RFC3339))
	d.publish("workload.started", &l)
	reply(w, http.StatusCreated, l)
}

// stream writes events as NDJSON until the client goes away.
func (d *Daemon) stream(w http.ResponseWriter, r *http.Request) {
	ch, stop := d.bus.Subscribe(64)
	defer stop()
	w.Header().Set("Content-Type", "application/x-ndjson")
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	first, _ := json.Marshal(d.event("status", nil))
	_, _ = w.Write(append(first, '\n'))
	flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(append(m.Body, '\n')); err != nil {
				return
			}
			flush()
		}
	}
}

// decode reads a JSON body and parses the duration field it points at.
func decode(w http.ResponseWriter, r *http.Request, v any, dur func() string) (time.Duration, bool) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return 0, false
	}
	if dur() == "" {
		return 0, true
	}
	d, err := time.ParseDuration(dur())
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return 0, false
	}
	return d, true
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	reply(w, code, map[string]string{"error": msg})
}

type peerKey struct{}

func withPeer(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, peerKey{}, connPID(c))
}

func peerPID(ctx context.Context) int {
	pid, _ := ctx.Value(peerKey{}).(int)
	return pid
}
