package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/KilianSen/used/internal/config"
	"github.com/KilianSen/used/internal/daemon"
	"github.com/KilianSen/used/internal/tray"
)

var stateColor = map[string]color.RGBA{
	"active":  {52, 199, 89, 255},
	"present": {255, 159, 10, 255},
	"unused":  {142, 142, 147, 255},
}

// cmdTray shows a status icon that follows the daemon and offers hold/release.
// All UI lives in internal/tray; this file only maps events to menus.
func cmdTray(cfg config.Config, args []string) error {
	fs := flags("tray")
	bg := fs.Bool("background", false, "detach from the console (Windows autostart)")
	_ = fs.Parse(args)
	if *bg {
		background()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := dial(cfg)
	updates := make(chan tray.Menu, 1)
	go follow(ctx, c, updates)

	onClick := func(id string) {
		if id == "quit" {
			stop()
			return
		}
		mode, dur, _ := strings.Cut(id, ":")
		req := daemon.OverrideRequest{Mode: mode, For: dur}
		var ev daemon.Event
		if err := c.post("/v1/override", req, &ev); err == nil {
			push(updates, menuFor(&ev))
		}
	}

	// At login the tray host (Explorer, the SNI watcher) may not be up yet.
	var err error
	for range 12 {
		if err = tray.Run(ctx, menuFor(nil), updates, onClick); err == nil || ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
	return err
}

// follow streams daemon events into menus, reconnecting while the daemon is away.
func follow(ctx context.Context, c *client, out chan tray.Menu) {
	for ctx.Err() == nil {
		var ev daemon.Event
		if c.get("/v1/status", &ev) == nil {
			push(out, menuFor(&ev))
			_ = c.stream("/v1/events", func(line []byte) {
				var ev daemon.Event
				if json.Unmarshal(line, &ev) == nil {
					push(out, menuFor(&ev))
				}
			})
		}
		push(out, menuFor(nil))
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

// push replaces any menu the UI hasn't picked up yet: only the latest matters.
func push(ch chan tray.Menu, m tray.Menu) {
	for {
		select {
		case ch <- m:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}

func menuFor(ev *daemon.Event) tray.Menu {
	quit := tray.Item{ID: "quit", Label: "Quit tray"}
	if ev == nil {
		return tray.Menu{
			Icon:    tray.Dot(32, color.RGBA{90, 90, 90, 255}, false),
			Tooltip: "used: daemon not running",
			Items:   []tray.Item{{Label: "Daemon not running"}, {}, quit},
		}
	}
	p := ev.Presence
	state := string(p.State)
	header := fmt.Sprintf("%s%s since %s", strings.ToUpper(state[:1]), state[1:], p.Since.Local().Format("15:04"))
	items := []tray.Item{{Label: header}}
	if n := len(ev.Workloads); n > 0 {
		tags := make([]string, n)
		for i, l := range ev.Workloads {
			tags[i] = l.Tag
		}
		items = append(items, tray.Item{Label: fmt.Sprintf("%d workload(s): %s", n, strings.Join(tags, ", "))})
	}
	if m := ev.Metrics; m != nil {
		items = append(items, tray.Item{Label: fmt.Sprintf("CPU %.0f%% · GPU %.0f%%", m.Total.CPU, m.Total.GPU)})
	}
	o := p.Override
	timed := o.Until != nil
	items = append(items, tray.Item{},
		tray.Item{ID: "hold:1h", Label: "Hold for 1 hour", Checked: o.Mode == "hold" && timed},
		tray.Item{ID: "hold", Label: "Hold until I say so", Checked: o.Mode == "hold" && !timed},
		tray.Item{ID: "release", Label: "Release (I'm leaving)", Checked: o.Mode == "release"},
		tray.Item{ID: "none", Label: "Automatic", Checked: o.Mode == "" || o.Mode == "none"},
		tray.Item{}, quit)

	c, ok := stateColor[state]
	if !ok {
		c = stateColor["unused"]
	}
	return tray.Menu{
		Icon:    tray.Dot(32, c, len(ev.Workloads) > 0),
		Tooltip: "used: " + state,
		Items:   items,
	}
}
