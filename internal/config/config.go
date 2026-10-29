// Package config loads used's TOML config. A preset supplies defaults; any
// key set in the file overrides it.
package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/KilianSen/used/internal/mqtt"
	"github.com/KilianSen/used/internal/notify"
	"github.com/KilianSen/used/internal/presence"
	"github.com/KilianSen/used/internal/sense"
	"github.com/KilianSen/used/internal/webhook"
	"github.com/KilianSen/used/internal/workload"
)

type Config struct {
	Socket   string `toml:"socket"`
	Presence struct {
		Preset           string             `toml:"preset"`
		IdleAfter        time.Duration      `toml:"idle_after"`
		UnusedAfter      time.Duration      `toml:"unused_after"`
		PresentMax       time.Duration      `toml:"present_max"`
		EvidenceHold     time.Duration      `toml:"evidence_hold"`
		LockIsUnused     bool               `toml:"lock_is_unused"`
		CountRemoteInput bool               `toml:"count_remote_input"`
		LinuxRawInput    bool               `toml:"linux_raw_input"`
		Signals          map[string]float64 `toml:"signals"` // evidence kind → weight, 0 disables
		Unattributed     float64            `toml:"unattributed"`
	} `toml:"presence"`
	Ignore    []workload.Rule `toml:"ignore"`
	Workloads struct {
		MaxTTL time.Duration `toml:"max_ttl"`
	} `toml:"workloads"`
	Report struct {
		Heartbeat time.Duration     `toml:"heartbeat"`
		AppNames  bool              `toml:"app_names"`
		Labels    map[string]string `toml:"labels"`
	} `toml:"report"`
	Webhook []webhook.Config `toml:"webhook"`
	MQTT    mqtt.Config      `toml:"mqtt"`
	Notify  notify.Config    `toml:"notify"`
}

// Default returns the config for a preset: strict, balanced (default) or lenient.
func Default(preset string) (Config, error) {
	var c Config
	p := &c.Presence
	p.Preset = preset
	p.IdleAfter, p.UnusedAfter, p.PresentMax = 2*time.Minute, 10*time.Minute, 45*time.Minute
	p.EvidenceHold = 30 * time.Second
	p.LockIsUnused = true
	p.Signals = map[string]float64{
		sense.KindFullscreen: 1.0,
		sense.KindCamera:     1.0,
		sense.KindAudio:      0.7,
		sense.KindAssertion:  0.6,
	}
	c.Workloads.MaxTTL = 24 * time.Hour
	c.Report.Heartbeat = time.Minute
	c.MQTT.QoS = 1

	switch preset {
	case "", "balanced":
		p.Preset = "balanced"
	case "strict": // hardware input only: no evidence, no grace
		p.UnusedAfter, p.PresentMax = p.IdleAfter, p.IdleAfter
		p.Signals = map[string]float64{}
	case "lenient":
		p.PresentMax = 2 * time.Hour
		p.Unattributed = 0.5
	default:
		return c, fmt.Errorf("unknown preset %q (strict, balanced, lenient)", preset)
	}
	return c, nil
}

// Path is $USED_CONFIG or <user config dir>/used/config.toml.
func Path() string {
	if p := os.Getenv("USED_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(dir, "used", "config.toml")
}

// Load reads path; a missing file yields the balanced defaults.
func Load(path string) (Config, error) {
	var head struct {
		Presence struct{ Preset string } `toml:"presence"`
	}
	_, err := toml.DecodeFile(path, &head)
	if errors.Is(err, fs.ErrNotExist) {
		return Default("")
	}
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	c, err := Default(head.Presence.Preset)
	if err != nil {
		return c, err
	}
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return c, fmt.Errorf("%s: unknown keys %v", path, undec)
	}
	return c, c.validate()
}

func (c Config) validate() error {
	p := c.Presence
	if p.IdleAfter <= 0 || p.UnusedAfter < p.IdleAfter || p.PresentMax < p.UnusedAfter {
		return errors.New("presence: need 0 < idle_after <= unused_after <= present_max")
	}
	if c.Report.Heartbeat < time.Second {
		return errors.New("report.heartbeat must be at least 1s")
	}
	return nil
}

// PresenceConfig maps the file config to the state machine's config.
func (c Config) PresenceConfig() presence.Config {
	p := c.Presence
	return presence.Config{
		IdleAfter: p.IdleAfter, UnusedAfter: p.UnusedAfter, PresentMax: p.PresentMax,
		EvidenceHold: p.EvidenceHold, LockIsUnused: p.LockIsUnused,
		CountRemoteInput: p.CountRemoteInput, Weights: p.Signals, Unattributed: p.Unattributed,
	}
}

// SocketPath is where the daemon listens: per user, private.
func (c Config) SocketPath() string {
	if c.Socket != "" {
		return c.Socket
	}
	if p := os.Getenv("USED_SOCKET"); p != "" {
		return p
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "used.sock")
	}
	if runtime.GOOS == "linux" {
		return fmt.Sprintf("/tmp/used-%d.sock", os.Getuid())
	}
	return filepath.Join(os.TempDir(), "used.sock") // per-user on macOS and Windows
}

// MachineID returns a stable random ID, created on first use.
func MachineID() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "unknown"
	}
	path := filepath.Join(dir, "used", "machine-id")
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return string(b)
	}
	id := strings.ToLower(rand.Text())
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(id), 0o644)
	return id
}
