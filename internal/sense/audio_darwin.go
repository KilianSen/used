//go:build darwin

package sense

import (
	"unsafe"

	"github.com/KilianSen/used/internal/macos"
)

// coreAudio lists processes with running audio output (playback) or input
// (microphone) via CoreAudio process objects. macOS 14+; older systems
// simply report nothing.
//
// Caveat: some apps keep an output stream open while silent (chat apps,
// browsers right after playback). Weight `audio` accordingly.

type audioAddr struct{ Selector, Scope, Element uint32 }

const (
	audioSystemObject  = 1
	audioScopeGlobal   = 0x676c6f62 // 'glob'
	audioProcessList   = 0x70727323 // 'prs#' kAudioHardwarePropertyProcessObjectList
	audioProcessPID    = 0x70706964 // 'ppid' kAudioProcessPropertyPID
	audioRunningOutput = 0x7069726f // 'piro' kAudioProcessPropertyIsRunningOutput
	audioRunningInput  = 0x70697269 // 'piri' kAudioProcessPropertyIsRunningInput
)

type coreAudio struct {
	size func(obj uint32, addr *audioAddr, qs uint32, q uintptr, size *uint32) int32
	data func(obj uint32, addr *audioAddr, qs uint32, q uintptr, size *uint32, out unsafe.Pointer) int32
}

func newCoreAudio() *coreAudio {
	a := &coreAudio{}
	macos.Func(&a.size, macos.CoreAudio, "AudioObjectGetPropertyDataSize")
	macos.Func(&a.data, macos.CoreAudio, "AudioObjectGetPropertyData")
	return a
}

func (a *coreAudio) u32(obj, sel uint32) uint32 {
	var v uint32
	size := uint32(4)
	if a.data(obj, &audioAddr{sel, audioScopeGlobal, 0}, 0, 0, &size, unsafe.Pointer(&v)) != 0 {
		return 0
	}
	return v
}

func (a *coreAudio) evidence() []Evidence {
	addr := &audioAddr{audioProcessList, audioScopeGlobal, 0}
	var size uint32
	if a.size(audioSystemObject, addr, 0, 0, &size) != 0 || size < 4 {
		return nil
	}
	ids := make([]uint32, size/4)
	if a.data(audioSystemObject, addr, 0, 0, &size, unsafe.Pointer(&ids[0])) != 0 {
		return nil
	}
	var out []Evidence
	for _, id := range ids[:size/4] {
		playing, recording := a.u32(id, audioRunningOutput) != 0, a.u32(id, audioRunningInput) != 0
		if !playing && !recording {
			continue
		}
		pid := int(int32(a.u32(id, audioProcessPID)))
		if playing {
			out = append(out, Evidence{Kind: KindAudio, PID: pid})
		}
		if recording {
			out = append(out, Evidence{Kind: KindCamera, PID: pid, Detail: "microphone"})
		}
	}
	return out
}
