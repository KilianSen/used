package workload

import (
	"fmt"
	"os"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// Proc is what rules match against. Expensive fields are loaded lazily.
type Proc struct {
	PID, PPID int
	Name      string
	exe       func() string
	cgroup    func() string
}

// Procs is a per-pass cache of process info. Create one per tick and drop it;
// PIDs get reused, so it must not live long.
type Procs struct{ m map[int]Proc }

func NewProcs() *Procs { return &Procs{m: map[int]Proc{}} }

func (c *Procs) Get(pid int) Proc {
	if p, ok := c.m[pid]; ok {
		return p
	}
	p := Proc{PID: pid, exe: func() string { return "" }, cgroup: func() string { return "" }}
	if h, err := process.NewProcess(int32(pid)); err == nil {
		ppid, _ := h.Ppid()
		p.PPID = int(ppid)
		p.Name, _ = h.Name()
		p.exe = once(func() string { s, _ := h.Exe(); return s })
		p.cgroup = once(func() string { return cgroupOf(pid) })
	}
	c.m[pid] = p
	return p
}

// Alive reports whether a process exists.
func Alive(pid int) bool { ok, _ := process.PidExists(int32(pid)); return ok }

// cgroupOf returns the cgroup v2 path (Linux), or "" elsewhere.
func cgroupOf(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			return path
		}
	}
	return ""
}

func once(f func() string) func() string {
	var v string
	var done bool
	return func() string {
		if !done {
			v, done = f(), true
		}
		return v
	}
}
