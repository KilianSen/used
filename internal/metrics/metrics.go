// Package metrics samples machine load and splits it into user vs workload,
// so a scheduler can see how busy the machine is *without* its own jobs.
//
// TODO: thermals and power.
package metrics

import (
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

// Bucket is CPU and GPU in percent of the whole machine, memory as a fraction of RAM.
type Bucket struct {
	CPU     float64 `json:"cpu"`
	GPU     float64 `json:"gpu"`
	MemUsed float64 `json:"mem_used"`
}

type Workload struct {
	Bucket
	ByTag map[string]Bucket `json:"by_tag,omitempty"`
}

type Sample struct {
	Total    Bucket   `json:"total"`
	User     Bucket   `json:"user"`
	Workload Workload `json:"workload"`
	GPUs     []GPU    `json:"gpus,omitempty"`
	Load1    float64  `json:"load1,omitempty"`
	MemTotal uint64   `json:"mem_total"`
	CPUs     int      `json:"cpus"`
	// Attribution says how the user/workload split was made for GPU:
	// "exact" (per-process counters) or "none" (all GPU load counts as user).
	Attribution struct {
		GPU string `json:"gpu"`
	} `json:"attribution"`
}

// OwnerFunc tells the collector which workload (if any) owns a PID.
type OwnerFunc func(pid int) (tag string, ok bool)

// Collector keeps per-process CPU times between samples to compute rates.
type Collector struct {
	prevCPU map[int32]float64
	prevAt  time.Time
	gpu     gpuReader
}

func NewCollector() *Collector {
	_, _ = cpu.Percent(0, false) // prime: the next call measures since now
	c := &Collector{prevCPU: map[int32]float64{}, gpu: newGPUReader()}
	c.gpu.read() // prime rate counters
	return c
}

// Collect samples the machine. owner may be nil when there are no workloads.
func (c *Collector) Collect(owner OwnerFunc) Sample {
	s := Sample{CPUs: runtime.NumCPU()}
	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		s.Total.CPU = round1(pct[0])
	}
	vm, err := mem.VirtualMemory()
	if err == nil {
		s.MemTotal = vm.Total
		s.Total.MemUsed = round3(vm.UsedPercent / 100)
	}
	if l, err := load.Avg(); err == nil {
		s.Load1 = l.Load1
	}
	gpus, gpuPID := c.gpu.read()
	s.GPUs, s.Total.GPU = gpus, round1(meanUtil(gpus))
	s.Attribution.GPU = "none"
	if gpuPID != nil {
		s.Attribution.GPU = "exact"
	}
	if owner != nil && s.MemTotal > 0 {
		s.Workload = c.workload(owner, float64(s.MemTotal), gpuPID, len(gpus))
	}
	s.User = Bucket{
		CPU:     round1(max(0, s.Total.CPU-s.Workload.CPU)),
		GPU:     round1(max(0, s.Total.GPU-s.Workload.GPU)),
		MemUsed: round3(max(0, s.Total.MemUsed-s.Workload.MemUsed)),
	}
	return s
}

func (c *Collector) workload(owner OwnerFunc, memTotal float64, gpuPID map[int]float64, nGPU int) Workload {
	now := time.Now()
	elapsed := now.Sub(c.prevAt).Seconds()
	first := c.prevAt.IsZero()
	c.prevAt = now

	w := Workload{ByTag: map[string]Bucket{}}
	pids, _ := process.Pids()
	cur := map[int32]float64{}
	for _, pid := range pids {
		tag, ok := owner(int(pid))
		if !ok {
			continue
		}
		p, err := process.NewProcess(pid)
		if err != nil {
			continue
		}
		b := w.ByTag[tag]
		if t, err := p.Times(); err == nil {
			cur[pid] = t.User + t.System
			if prev, seen := c.prevCPU[pid]; seen && !first && elapsed > 0 {
				b.CPU += (cur[pid] - prev) / elapsed / float64(runtime.NumCPU()) * 100
			}
		}
		if mi, err := p.MemoryInfo(); err == nil {
			b.MemUsed += float64(mi.RSS) / memTotal
		}
		if nGPU > 0 {
			b.GPU += gpuPID[int(pid)] / float64(nGPU)
		}
		w.ByTag[tag] = b
	}
	c.prevCPU = cur

	for tag, b := range w.ByTag {
		b.CPU, b.GPU, b.MemUsed = round1(b.CPU), round1(min(100, b.GPU)), round3(b.MemUsed)
		w.ByTag[tag] = b
		w.CPU += b.CPU
		w.GPU += b.GPU
		w.MemUsed += b.MemUsed
	}
	w.CPU, w.GPU, w.MemUsed = round1(w.CPU), round1(min(100, w.GPU)), round3(w.MemUsed)
	return w
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
func round3(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }
