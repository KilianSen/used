package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KilianSen/used/internal/bus"
	"github.com/KilianSen/used/internal/config"
	"github.com/KilianSen/used/internal/metrics"
	"github.com/KilianSen/used/internal/mqtt"
	"github.com/KilianSen/used/internal/notify"
	"github.com/KilianSen/used/internal/presence"
	"github.com/KilianSen/used/internal/sense"
	"github.com/KilianSen/used/internal/webhook"
	"github.com/KilianSen/used/internal/workload"
)

const PayloadVersion = 1

// Event is the payload sent to webhooks and API streams. See DESIGN.md §6.2.
type Event struct {
	Version   int              `json:"version"`
	Event     string           `json:"event"`
	ID        string           `json:"id"`
	Seq       uint64           `json:"seq"`
	TS        time.Time        `json:"ts"`
	Machine   MachineInfo      `json:"machine"`
	Presence  presence.Status  `json:"presence"`
	Workloads []workload.Lease `json:"workloads"`
	Metrics   *metrics.Sample  `json:"metrics,omitempty"`
	Lease     *workload.Lease  `json:"lease,omitempty"` // workload.started / workload.ended
}

type MachineInfo struct {
	ID       string            `json:"id"`
	Hostname string            `json:"hostname"`
	OS       string            `json:"os"`
	Arch     string            `json:"arch"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// Daemon ticks the engine, samples metrics and serves the local API.
type Daemon struct {
	cfg     config.Config
	log     *slog.Logger
	engine  *Engine
	reg     *workload.Registry
	metrics *metrics.Collector
	bus     *bus.Bus
	machine MachineInfo
	seq     atomic.Uint64

	mu   sync.RWMutex
	last Snapshot
	smp  metrics.Sample
	stop context.CancelFunc
}

// Build wires an engine from config. Shared by the daemon and one-shot CLI.
func Build(cfg config.Config) (*Engine, *workload.Registry, error) {
	probe, err := sense.New(sense.Options{RawInput: cfg.Presence.LinuxRawInput})
	if err != nil {
		return nil, nil, err
	}
	reg, err := workload.NewRegistry(cfg.Ignore, cfg.Workloads.MaxTTL)
	if err != nil {
		return nil, nil, err
	}
	return NewEngine(probe, presence.New(cfg.PresenceConfig()), reg, cfg.Report.AppNames), reg, nil
}

func New(cfg config.Config, log *slog.Logger) (*Daemon, error) {
	engine, reg, err := Build(cfg)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	return &Daemon{
		cfg: cfg, log: log, engine: engine, reg: reg,
		metrics: metrics.NewCollector(), bus: bus.New(),
		machine: MachineInfo{ID: config.MachineID(), Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, Labels: cfg.Report.Labels},
	}, nil
}

func (d *Daemon) Run(ctx context.Context) error {
	ctx, d.stop = context.WithCancel(ctx)
	defer d.stop()
	ln, err := listen(d.cfg.SocketPath())
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: d.routes(), ConnContext: withPeer}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = os.Remove(d.cfg.SocketPath())
	}()

	// Build every sink before starting any, so a config error leaks nothing.
	var sinks []interface {
		Run(context.Context, <-chan bus.Msg)
	}
	for _, wc := range d.cfg.Webhook {
		s, err := webhook.New(wc, d.log)
		if err != nil {
			return err
		}
		sinks = append(sinks, s)
	}
	if d.cfg.MQTT.Broker != "" {
		s, err := mqtt.New(d.cfg.MQTT, mqtt.Device{ID: d.machine.ID, Hostname: d.machine.Hostname,
			Model: d.machine.OS + "/" + d.machine.Arch}, d.log)
		if err != nil {
			return err
		}
		sinks = append(sinks, s)
	}
	if d.cfg.Notify.Enabled {
		sinks = append(sinks, notify.NewSink(d.cfg.Notify, d.log))
	}
	var wg sync.WaitGroup
	defer func() { d.stop(); wg.Wait() }() // let sinks say goodbye (MQTT: offline)
	for _, s := range sinks {
		ch, stop := d.bus.Subscribe(1024)
		defer stop()
		wg.Go(func() { s.Run(ctx, ch) })
	}

	d.log.Info("used is watching", "socket", d.cfg.SocketPath(), "sinks", len(sinks), "preset", d.cfg.Presence.Preset)
	d.sample()
	d.step()
	d.publish("daemon.started", nil)

	tick := time.NewTicker(time.Second)
	beat := time.NewTicker(d.cfg.Report.Heartbeat)
	defer tick.Stop()
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			d.step()
		case <-beat.C:
			d.sample()
			d.publish("heartbeat", nil)
		}
	}
}

func (d *Daemon) step() {
	snap := d.engine.Tick()
	d.mu.Lock()
	d.last = snap
	d.mu.Unlock()
	for _, l := range snap.Ended {
		d.publish("workload.ended", &l)
	}
	if snap.Changed {
		s := snap.Status
		d.log.Info("state changed", "from", s.Previous, "to", s.State, "reasons", strings.Join(s.Reasons, ","))
		d.publish("state.changed", nil)
	}
}

func (d *Daemon) sample() {
	var owner metrics.OwnerFunc
	if !d.reg.Empty() {
		procs := workload.NewProcs()
		owner = func(pid int) (string, bool) { return d.reg.Owner(pid, procs) }
	}
	s := d.metrics.Collect(owner)
	d.mu.Lock()
	d.smp = s
	d.mu.Unlock()
}

// event builds a payload from the latest state. Only published events take a
// sequence number, so receivers can use gaps to detect loss.
func (d *Daemon) event(kind string, lease *workload.Lease) Event {
	d.mu.RLock()
	st, smp := d.last.Status, d.smp
	d.mu.RUnlock()
	st.Trace = nil
	return Event{
		Version: PayloadVersion, Event: kind, ID: "evt_" + strings.ToLower(rand.Text()[:16]),
		Seq: d.seq.Load(), TS: time.Now().UTC(), Machine: d.machine, Presence: st,
		Workloads: d.reg.Leases(), Metrics: &smp, Lease: lease,
	}
}

func (d *Daemon) publish(kind string, lease *workload.Lease) {
	ev := d.event(kind, lease)
	ev.Seq = d.seq.Add(1)
	body, err := json.Marshal(ev)
	if err != nil {
		d.log.Error("encode event", "err", err)
		return
	}
	d.bus.Publish(bus.Msg{Type: kind, ID: ev.ID, Body: body})
}

// listen opens the API socket, refusing to steal it from a running daemon.
func listen(path string) (net.Listener, error) {
	if len(path) > 103 { // sun_path limit on macOS (Linux: 107)
		return nil, fmt.Errorf("socket path too long (%d bytes, max 103): %s", len(path), path)
	}
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return nil, fmt.Errorf("another used daemon is already listening on %s", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return ln, nil
}
