package metrics

import "github.com/KilianSen/used/internal/macos"

// macOS: IOAccelerator's PerformanceStatistics (same data Activity Monitor
// uses). No per-process attribution is exposed.
type ioAccel struct {
	matching func(name string) uintptr
	services func(port uint32, matching uintptr, it *uint32) int32
	next     func(it uint32) uint32
	property func(entry uint32, key, alloc uintptr, opts uint32) uintptr
	release  func(obj uint32) int32

	keyStats, keyModel, keyUtil, keyMem uintptr
}

func newGPUReader() gpuReader {
	if macos.Load() != nil {
		return noGPU{}
	}
	a := &ioAccel{}
	macos.Func(&a.matching, macos.IOKit, "IOServiceMatching")
	macos.Func(&a.services, macos.IOKit, "IOServiceGetMatchingServices")
	macos.Func(&a.next, macos.IOKit, "IOIteratorNext")
	macos.Func(&a.property, macos.IOKit, "IORegistryEntryCreateCFProperty")
	macos.Func(&a.release, macos.IOKit, "IOObjectRelease")
	a.keyStats = macos.Str("PerformanceStatistics")
	a.keyModel = macos.Str("model")
	a.keyUtil = macos.Str("Device Utilization %")
	a.keyMem = macos.Str("In use system memory")
	return a
}

func (a *ioAccel) read() ([]GPU, map[int]float64) {
	var it uint32
	if a.services(0, a.matching("IOAccelerator"), &it) != 0 { // consumes the matching dict
		return nil, nil
	}
	defer a.release(it)

	var gpus []GPU
	for svc := a.next(it); svc != 0; svc = a.next(it) {
		stats := a.property(svc, a.keyStats, 0, 0)
		model := a.property(svc, a.keyModel, 0, 0)
		if stats != 0 {
			gpus = append(gpus, GPU{
				Name:    macos.String(model),
				Util:    float64(macos.Int(macos.Get(stats, a.keyUtil))),
				MemUsed: uint64(macos.Int(macos.Get(stats, a.keyMem))),
			})
		}
		macos.Release(stats)
		macos.Release(model)
		a.release(svc)
	}
	return gpus, nil
}
