package metrics

import (
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows: the same performance counters Task Manager uses. They cover every
// vendor and carry the PID in each instance name:
//   pid_1234_luid_0x0000_0x0000D1E2_phys_0_eng_0_engtype_3D
// Per adapter, utilization is the busiest engine type summed over processes.

var (
	pdh            = windows.NewLazySystemDLL("pdh.dll")
	pPdhOpenQuery  = pdh.NewProc("PdhOpenQueryW")
	pPdhAddCounter = pdh.NewProc("PdhAddEnglishCounterW")
	pPdhCollect    = pdh.NewProc("PdhCollectQueryData")
	pPdhGetArray   = pdh.NewProc("PdhGetFormattedCounterArrayW")
)

const (
	pdhFmtDouble = 0x00000200
	pdhMoreData  = 0x800007D2
)

// pdhItem mirrors PDH_FMT_COUNTERVALUE_ITEM_W with a double value.
type pdhItem struct {
	Name   *uint16
	Status uint32
	_      uint32
	Value  float64
}

type pdhGPU struct{ query, engine, memory uintptr }

func newGPUReader() gpuReader {
	g := &pdhGPU{}
	if r, _, _ := pPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&g.query))); r != 0 {
		return noGPU{}
	}
	add := func(path string, dst *uintptr) bool {
		p, _ := windows.UTF16PtrFromString(path)
		r, _, _ := pPdhAddCounter.Call(g.query, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(dst)))
		return r == 0
	}
	if !add(`\GPU Engine(*)\Utilization Percentage`, &g.engine) {
		return noGPU{}
	}
	add(`\GPU Adapter Memory(*)\Dedicated Usage`, &g.memory)
	pPdhCollect.Call(g.query) // rate counters need a baseline sample
	return g
}

func (g *pdhGPU) read() ([]GPU, map[int]float64) {
	if r, _, _ := pPdhCollect.Call(g.query); r != 0 {
		return nil, nil
	}
	type key struct{ luid, eng string }
	perEngine := map[key]float64{}            // adapter+engine type → summed util
	perPIDEng := map[int]map[string]float64{} // pid → engine key → util
	for name, v := range counterArray(g.engine) {
		pid, luid, eng := parseEngine(name)
		perEngine[key{luid, eng}] += v
		if pid > 0 {
			if perPIDEng[pid] == nil {
				perPIDEng[pid] = map[string]float64{}
			}
			perPIDEng[pid][luid+eng] += v
		}
	}

	byAdapter := map[string]*GPU{}
	for k, v := range perEngine {
		a := byAdapter[k.luid]
		if a == nil {
			a = &GPU{Name: k.luid}
			byAdapter[k.luid] = a
		}
		a.Util = min(100, max(a.Util, v))
	}
	if g.memory != 0 {
		for name, v := range counterArray(g.memory) {
			if a := byAdapter[luidOf(name)]; a != nil {
				a.MemUsed += uint64(v)
			}
		}
	}

	gpus := make([]GPU, 0, len(byAdapter))
	for _, a := range byAdapter {
		gpus = append(gpus, *a)
	}
	perPID := make(map[int]float64, len(perPIDEng))
	for pid, engs := range perPIDEng {
		for _, v := range engs {
			perPID[pid] = max(perPID[pid], v)
		}
	}
	return gpus, perPID
}

func counterArray(counter uintptr) map[string]float64 {
	var size, count uint32
	if r, _, _ := pPdhGetArray.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0); uint32(r) != pdhMoreData || size == 0 {
		return nil
	}
	buf := make([]byte, size)
	if r, _, _ := pPdhGetArray.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
		return nil
	}
	items := unsafe.Slice((*pdhItem)(unsafe.Pointer(&buf[0])), count)
	out := make(map[string]float64, count)
	for _, it := range items {
		out[windows.UTF16PtrToString(it.Name)] += it.Value
	}
	return out
}

// parseEngine splits "pid_1234_luid_0x0_0xD1E2_phys_0_eng_0_engtype_3D".
func parseEngine(name string) (pid int, luid, eng string) {
	if rest, ok := strings.CutPrefix(name, "pid_"); ok {
		pid, _ = strconv.Atoi(rest[:max(0, strings.IndexByte(rest, '_'))])
	}
	luid = luidOf(name)
	if i := strings.Index(name, "engtype_"); i >= 0 {
		eng = name[i+len("engtype_"):]
	}
	return pid, luid, eng
}

// luidOf extracts "luid_0x…_0x…" (the adapter id) from an instance name.
func luidOf(name string) string {
	i := strings.Index(name, "luid_")
	if i < 0 {
		return ""
	}
	parts := strings.SplitN(name[i:], "_", 4) // luid, 0xHigh, 0xLow, rest
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:3], "_")
}
