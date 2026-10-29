package metrics

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Linux: NVIDIA via NVML (loaded at runtime, absent = skipped) with per-PID
// utilization; AMD/Intel via the DRM sysfs counters.
//
// TODO: per-PID for AMD/Intel from /proc/<pid>/fdinfo drm-engine-* counters.

type linuxGPU struct{ nv *nvml }

func newGPUReader() gpuReader { return &linuxGPU{nv: loadNVML()} }

func (l *linuxGPU) read() ([]GPU, map[int]float64) {
	var gpus []GPU
	var perPID map[int]float64
	if l.nv != nil {
		gpus, perPID = l.nv.read()
	}
	return append(gpus, sysfsGPUs()...), perPID
}

// sysfsGPUs reads amdgpu / i915 / xe cards that expose gpu_busy_percent.
func sysfsGPUs() []GPU {
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*/device/gpu_busy_percent")
	var out []GPU
	for _, busy := range cards {
		dev := filepath.Dir(busy)
		g := GPU{Name: readTrim(dev + "/product_name"), Util: readNum(busy)}
		g.MemUsed, g.MemTotal = uint64(readNum(dev+"/mem_info_vram_used")), uint64(readNum(dev+"/mem_info_vram_total"))
		if temps, _ := filepath.Glob(dev + "/hwmon/hwmon*/temp1_input"); len(temps) > 0 {
			g.TempC = readNum(temps[0]) / 1000
		}
		out = append(out, g)
	}
	return out
}

func readTrim(path string) string { b, _ := os.ReadFile(path); return strings.TrimSpace(string(b)) }
func readNum(path string) float64 { f, _ := strconv.ParseFloat(readTrim(path), 64); return f }

// --- NVML ---

const (
	nvmlSuccess          = 0
	nvmlInsufficientSize = 7
)

type nvml struct {
	count     func(n *uint32) int32
	handle    func(i uint32, h *uintptr) int32
	name      func(h uintptr, buf *byte, n uint32) int32
	util      func(h uintptr, u *[2]uint32) int32 // gpu, memory
	mem       func(h uintptr, m *[3]uint64) int32 // total, free, used
	temp      func(h uintptr, sensor uint32, t *uint32) int32
	procUtil  func(h uintptr, samples unsafe.Pointer, n *uint32, since uint64) int32
	lastStamp map[uintptr]uint64
}

// nvmlProcSample mirrors nvmlProcessUtilizationSample_t.
type nvmlProcSample struct {
	PID               uint32
	Timestamp         uint64
	SM, Mem, Enc, Dec uint32
}

func loadNVML() *nvml {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return nil
	}
	lib, err := purego.Dlopen("libnvidia-ml.so.1", purego.RTLD_LAZY)
	if err != nil {
		return nil
	}
	var initFn func() int32
	purego.RegisterLibFunc(&initFn, lib, "nvmlInit_v2")
	if initFn() != nvmlSuccess {
		return nil
	}
	n := &nvml{lastStamp: map[uintptr]uint64{}}
	purego.RegisterLibFunc(&n.count, lib, "nvmlDeviceGetCount_v2")
	purego.RegisterLibFunc(&n.handle, lib, "nvmlDeviceGetHandleByIndex_v2")
	purego.RegisterLibFunc(&n.name, lib, "nvmlDeviceGetName")
	purego.RegisterLibFunc(&n.util, lib, "nvmlDeviceGetUtilizationRates")
	purego.RegisterLibFunc(&n.mem, lib, "nvmlDeviceGetMemoryInfo")
	purego.RegisterLibFunc(&n.temp, lib, "nvmlDeviceGetTemperature")
	purego.RegisterLibFunc(&n.procUtil, lib, "nvmlDeviceGetProcessUtilization")
	return n
}

func (n *nvml) read() ([]GPU, map[int]float64) {
	var count uint32
	if n.count(&count) != nvmlSuccess {
		return nil, nil
	}
	gpus, perPID := make([]GPU, 0, count), map[int]float64{}
	for i := range count {
		var h uintptr
		if n.handle(i, &h) != nvmlSuccess {
			continue
		}
		var g GPU
		name := make([]byte, 96)
		if n.name(h, &name[0], uint32(len(name))) == nvmlSuccess {
			g.Name = strings.TrimRight(string(name), "\x00")
		}
		var u [2]uint32
		if n.util(h, &u) == nvmlSuccess {
			g.Util = float64(u[0])
		}
		var m [3]uint64
		if n.mem(h, &m) == nvmlSuccess {
			g.MemTotal, g.MemUsed = m[0], m[2]
		}
		var t uint32
		if n.temp(h, 0, &t) == nvmlSuccess {
			g.TempC = float64(t)
		}
		gpus = append(gpus, g)
		n.processes(h, perPID)
	}
	return gpus, perPID
}

// processes adds per-PID SM utilization since the previous call.
func (n *nvml) processes(h uintptr, into map[int]float64) {
	var cnt uint32
	if r := n.procUtil(h, nil, &cnt, n.lastStamp[h]); r != nvmlInsufficientSize || cnt == 0 {
		return
	}
	samples := make([]nvmlProcSample, cnt)
	if n.procUtil(h, unsafe.Pointer(&samples[0]), &cnt, n.lastStamp[h]) != nvmlSuccess {
		return
	}
	for _, s := range samples[:cnt] {
		into[int(s.PID)] += float64(s.SM)
		n.lastStamp[h] = max(n.lastStamp[h], s.Timestamp)
	}
}
