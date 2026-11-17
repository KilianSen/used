package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/KilianSen/used/internal/daemon"
	"github.com/KilianSen/used/internal/metrics"
	"github.com/KilianSen/used/internal/presence"
	"github.com/KilianSen/used/internal/workload"
)

var tty = func() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == ""
}()

func paint(code, s string) string {
	if !tty {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func dim(s string) string  { return paint("2", s) }
func bold(s string) string { return paint("1", s) }

func badge(s presence.State) string {
	color := map[presence.State]string{presence.Active: "32", presence.Present: "33", presence.Unused: "34"}[s]
	return paint(color+";1", "● "+string(s))
}

func show(asJSON bool, v any, human func()) error {
	if asJSON {
		printJSON(v)
	} else {
		human()
	}
	return nil
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func row(label, value string) { fmt.Printf("  %-9s %s\n", dim(label), value) }

func printStatus(s presence.Status, m *metrics.Sample, leases []workload.Lease) {
	fmt.Printf("%s  since %s · confidence %.2f\n", badge(s.State), s.Since.Local().Format("15:04:05"), s.Confidence)
	row("input", fmt.Sprintf("idle %s via %s", time.Duration(s.IdleSeconds)*time.Second, s.InputSource))
	row("why", strings.Join(s.Reasons, ", "))
	if len(s.Evidence) > 0 {
		var ev []string
		for _, e := range s.Evidence {
			ev = append(ev, evidenceString(e.Kind, e.PID, e.Detail))
		}
		row("evidence", strings.Join(ev, "; "))
	}
	if s.Locked || s.Remote {
		row("session", fmt.Sprintf("locked=%t remote=%t", s.Locked, s.Remote))
	}
	if s.Override.Mode != "none" && s.Override.Mode != "" {
		o := s.Override.Mode
		if s.Override.Until != nil {
			o += " until " + s.Override.Until.Local().Format("15:04")
		}
		row("override", o)
	}
	if m != nil {
		row("cpu", fmt.Sprintf("%.1f%% (user %.1f%%, workload %.1f%%) · mem %.0f%% of %s",
			m.Total.CPU, m.User.CPU, m.Workload.CPU, m.Total.MemUsed*100, gib(m.MemTotal)))
		for _, g := range m.GPUs {
			line := fmt.Sprintf("%.0f%% %s", g.Util, dim(g.Name))
			if g.TempC > 0 {
				line += fmt.Sprintf(" · %.0f°C", g.TempC)
			}
			row("gpu", line)
		}
		if len(m.GPUs) > 0 {
			row("", dim(fmt.Sprintf("user %.1f%%, workload %.1f%% (attribution: %s)", m.User.GPU, m.Workload.GPU, m.Attribution.GPU)))
		}
	}
	for _, l := range leases {
		row("workload", fmt.Sprintf("%s (pid %d, until %s)", bold(l.Tag), l.PID, l.Expires.Local().Format("Jan 2 15:04")))
	}
}

func printTransition(at time.Time, s presence.Status) {
	from := string(s.Previous)
	if from == "" {
		from = "start"
	}
	fmt.Printf("%s  %-7s → %s  %s\n", dim(at.Local().Format("15:04:05")), from, badge(s.State),
		dim(fmt.Sprintf("%s · idle %ds via %s", strings.Join(s.Reasons, ","), s.IdleSeconds, s.InputSource)))
}

func printEventLine(line []byte) {
	var ev daemon.Event
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Event {
	case "state.changed", "status":
		printTransition(ev.TS, ev.Presence)
	case "heartbeat":
		cpu := 0.0
		if ev.Metrics != nil {
			cpu = ev.Metrics.Total.CPU
		}
		fmt.Printf("%s  %s %s\n", dim(ev.TS.Local().Format("15:04:05")), dim("♥"),
			dim(fmt.Sprintf("%s · idle %ds · cpu %.1f%%", ev.Presence.State, ev.Presence.IdleSeconds, cpu)))
	default:
		detail := ""
		if ev.Lease != nil {
			detail = fmt.Sprintf("%s (pid %d)", ev.Lease.Tag, ev.Lease.PID)
		}
		fmt.Printf("%s  %s %s\n", dim(ev.TS.Local().Format("15:04:05")), bold(ev.Event), detail)
	}
}

func printExplain(ex daemon.Explain) {
	printStatus(ex.Status, nil, ex.Leases)
	fmt.Println("\n" + bold("decision"))
	for _, t := range ex.Status.Trace {
		fmt.Println("  " + t)
	}
	if len(ex.Filtered) > 0 {
		fmt.Println("\n" + bold("ignored evidence") + dim(" (caused by workloads)"))
		for _, f := range ex.Filtered {
			fmt.Printf("  %s ← %s\n", evidenceString(f.Kind, f.PID, f.Detail), f.Tag)
		}
	}
	if len(ex.Rules) > 0 {
		fmt.Println("\n" + bold("ignore rules"))
		for _, r := range ex.Rules {
			b, _ := json.Marshal(r)
			warn := ""
			if r.Name != "" && r.Path == "" && r.Cgroup == "" {
				warn = paint("33", "  ⚠ name-only rules are spoofable")
			}
			fmt.Printf("  %s%s\n", b, warn)
		}
	}
}

func evidenceString(kind string, pid int, detail string) string {
	s := kind
	if pid > 0 {
		s += fmt.Sprintf(" pid %d", pid)
	}
	if detail != "" {
		s += fmt.Sprintf(" %q", detail)
	}
	return s
}

func gib(n uint64) string {
	return fmt.Sprintf("%.0f GiB", float64(n)/(1<<30))
}
