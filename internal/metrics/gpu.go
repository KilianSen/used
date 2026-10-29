package metrics

// GPU is one adapter's load.
type GPU struct {
	Name     string  `json:"name,omitempty"`
	Util     float64 `json:"util"`                // percent busy
	MemUsed  uint64  `json:"mem_used,omitempty"`  // bytes (shared memory on unified-memory Macs)
	MemTotal uint64  `json:"mem_total,omitempty"` // bytes, when known
	TempC    float64 `json:"temp_c,omitempty"`
}

// gpuReader is implemented per OS. perPID maps PID → percent of one GPU it
// keeps busy; nil means the platform can't attribute GPU load to processes.
type gpuReader interface {
	read() (gpus []GPU, perPID map[int]float64)
}

type noGPU struct{}

func (noGPU) read() ([]GPU, map[int]float64) { return nil, nil }

// meanUtil is the machine-wide GPU load: average over adapters.
func meanUtil(gpus []GPU) float64 {
	if len(gpus) == 0 {
		return 0
	}
	var sum float64
	for _, g := range gpus {
		sum += g.Util
	}
	return sum / float64(len(gpus))
}
