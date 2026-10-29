//go:build linux

package sense

import (
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/screensaver"
	"github.com/jezek/xgb/xproto"
)

// Linux has no single idle API, so we take the best source available:
//   raw /dev/input (opt-in, hardware only) → Mutter IdleMonitor (GNOME)
//   → X11 screensaver (X sessions only) → logind IdleHint (coarse).
// Lock state, remoteness and idle inhibitors come from logind.
//
// Audio/mic streams come from pactl (PulseAudio or PipeWire).
//
// TODO: ext-idle-notify-v1 for wlroots/KDE Wayland sessions.

type linuxProbe struct {
	raw    *evdev
	audio  pulse
	system *dbus.Conn
	user   *dbus.Conn
	x      *xgb.Conn
	xroot  xproto.Window
}

func newProbe(o Options) (Probe, error) {
	p := &linuxProbe{}
	if o.RawInput {
		p.raw, _ = startEvdev() // falls back silently; `used explain` shows the source
	}
	p.system, _ = dbus.ConnectSystemBus()
	p.user, _ = dbus.ConnectSessionBus()
	if os.Getenv("DISPLAY") != "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		// On Wayland, DISPLAY points at Xwayland, which only sees X clients' input.
		if x, err := xgb.NewConn(); err == nil && screensaver.Init(x) == nil {
			p.x, p.xroot = x, xproto.Setup(x).DefaultScreen(x).Root
		}
	}
	return p, nil
}

func (p *linuxProbe) Read() (Reading, error) {
	r := Reading{At: time.Now(), Source: SourceNone}
	s := p.session()
	r.Locked = s.locked || !s.active
	r.Remote = s.remote

	switch {
	case p.raw != nil && p.raw.seen():
		r.Idle, r.Source = p.raw.idle(), SourceHID
	case p.mutterIdle(&r.Idle):
		r.Source = SourceCompositor
	case p.x11Idle(&r.Idle):
		r.Source = SourceX11
	case s.found:
		r.Source = SourceLogind
		if s.idleHint && s.idleSince > 0 {
			r.Idle = time.Since(time.UnixMicro(int64(s.idleSince)))
		}
	}
	r.Evidence = append(p.inhibitors(), p.audio.evidence()...)
	return r, nil
}

func (p *linuxProbe) Close() error {
	for _, c := range []*dbus.Conn{p.system, p.user} {
		if c != nil {
			c.Close()
		}
	}
	if p.x != nil {
		p.x.Close()
	}
	return nil
}

type session struct {
	found, active, locked, remote, idleHint bool
	idleSince                               uint64
}

// session finds this user's display session via logind. Works from a
// systemd --user service too, which is not itself part of a session.
func (p *linuxProbe) session() session {
	var s session
	if p.system == nil {
		return s
	}
	login := p.system.Object("org.freedesktop.login1", "/org/freedesktop/login1")
	var userPath dbus.ObjectPath
	if login.Call("org.freedesktop.login1.Manager.GetUser", 0, uint32(os.Getuid())).Store(&userPath) != nil {
		return s
	}
	v, err := p.system.Object("org.freedesktop.login1", userPath).GetProperty("org.freedesktop.login1.User.Display")
	if err != nil {
		return s
	}
	disp, ok := v.Value().([]any)
	if !ok || len(disp) != 2 {
		return s
	}
	path, _ := disp[1].(dbus.ObjectPath)
	if path == "" || path == "/" {
		return s
	}
	var props map[string]dbus.Variant
	if p.system.Object("org.freedesktop.login1", path).
		Call("org.freedesktop.DBus.Properties.GetAll", 0, "org.freedesktop.login1.Session").Store(&props) != nil {
		return s
	}
	get := func(k string) any { return props[k].Value() }
	s.found = true
	s.active, _ = get("Active").(bool)
	s.locked, _ = get("LockedHint").(bool)
	s.remote, _ = get("Remote").(bool)
	s.idleHint, _ = get("IdleHint").(bool)
	s.idleSince, _ = get("IdleSinceHint").(uint64)
	return s
}

func (p *linuxProbe) mutterIdle(out *time.Duration) bool {
	if p.user == nil {
		return false
	}
	var ms uint64
	err := p.user.Object("org.gnome.Mutter.IdleMonitor", "/org/gnome/Mutter/IdleMonitor/Core").
		Call("org.gnome.Mutter.IdleMonitor.GetIdletime", 0).Store(&ms)
	*out = time.Duration(ms) * time.Millisecond
	return err == nil
}

func (p *linuxProbe) x11Idle(out *time.Duration) bool {
	if p.x == nil {
		return false
	}
	info, err := screensaver.QueryInfo(p.x, xproto.Drawable(p.xroot)).Reply()
	if err != nil {
		return false
	}
	*out = time.Duration(info.MsSinceUserInput) * time.Millisecond
	return true
}

// inhibitors: logind "idle" block inhibitors (video players, calls, …).
func (p *linuxProbe) inhibitors() []Evidence {
	if p.system == nil {
		return nil
	}
	var list []struct {
		What, Who, Why, Mode string
		UID, PID             uint32
	}
	if p.system.Object("org.freedesktop.login1", "/org/freedesktop/login1").
		Call("org.freedesktop.login1.Manager.ListInhibitors", 0).Store(&list) != nil {
		return nil
	}
	var out []Evidence
	for _, in := range list {
		if in.Mode == "block" && strings.Contains(in.What, "idle") {
			out = append(out, Evidence{Kind: KindAssertion, PID: int(in.PID), Detail: in.Why})
		}
	}
	return out
}
