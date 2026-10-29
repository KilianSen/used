//go:build !darwin && !linux && !windows

package metrics

func newGPUReader() gpuReader { return noGPU{} }
