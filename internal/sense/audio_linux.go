//go:build linux

package sense

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"time"
)

// pulse lists uncorked playback streams and recording streams via pactl,
// which talks to both PulseAudio and PipeWire (pipewire-pulse). Cached for a
// few seconds since it forks a process.
type pulse struct {
	at       time.Time
	cached   []Evidence
	disabled bool
}

func (a *pulse) evidence() []Evidence {
	if a.disabled || time.Since(a.at) < 3*time.Second {
		return a.cached
	}
	a.at, a.cached = time.Now(), nil
	for _, src := range []struct{ list, kind, detail string }{
		{"sink-inputs", KindAudio, ""},
		{"source-outputs", KindCamera, "microphone"},
	} {
		out, err := exec.Command("pactl", "-f", "json", "list", src.list).Output()
		if errors.Is(err, exec.ErrNotFound) {
			a.disabled = true
			return nil
		}
		var streams []struct {
			Corked     bool              `json:"corked"`
			Properties map[string]string `json:"properties"`
		}
		if err != nil || json.Unmarshal(out, &streams) != nil {
			continue
		}
		for _, s := range streams {
			if s.Corked {
				continue
			}
			pid, _ := strconv.Atoi(s.Properties["application.process.id"])
			a.cached = append(a.cached, Evidence{Kind: src.kind, PID: pid, Detail: src.detail})
		}
	}
	return a.cached
}
