//go:build windows

package sense

import (
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows: GetLastInputInfo also counts SendInput, so hardware input comes from
// low-level hooks that drop events flagged as injected. GetLastInputInfo only
// seeds the clock at startup, before the first hardware event arrives.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pSetWindowsHookEx    = user32.NewProc("SetWindowsHookExW")
	pCallNextHookEx      = user32.NewProc("CallNextHookEx")
	pGetMessage          = user32.NewProc("GetMessageW")
	pGetLastInputInfo    = user32.NewProc("GetLastInputInfo")
	pOpenInputDesktop    = user32.NewProc("OpenInputDesktop")
	pCloseDesktop        = user32.NewProc("CloseDesktop")
	pGetUserObjectInfo   = user32.NewProc("GetUserObjectInformationW")
	pGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	pGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	pGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	pQueryNotification   = shell32.NewProc("SHQueryUserNotificationState")
	pGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	pGetTickCount        = kernel32.NewProc("GetTickCount")
)

const (
	whKeyboardLL     = 13
	whMouseLL        = 14
	llkhfInjected    = 0x10
	llmhfInjected    = 0x01
	smRemoteSession  = 0x1000
	desktopSwitch    = 0x0100
	uoiName          = 2
	qunsBusy         = 2 // a fullscreen app is running
	qunsD3DFull      = 3 // exclusive-mode Direct3D
	qunsPresentation = 4
)

type kbdLL struct {
	VK, Scan, Flags, Time uint32
	Extra                 uintptr
}
type mouseLL struct {
	X, Y              int32
	Data, Flags, Time uint32
	Extra             uintptr
}
type winMsg struct {
	HWnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

type winProbe struct {
	lastHW atomic.Int64 // unix nanos of last hardware input
	seeded atomic.Bool  // lastHW still comes from GetLastInputInfo
}

func newProbe(Options) (Probe, error) {
	p := &winProbe{}
	if idle, ok := osIdle(); ok {
		p.lastHW.Store(time.Now().Add(-idle).UnixNano())
		p.seeded.Store(true)
	}
	go p.hookLoop()
	return p, nil
}

func (p *winProbe) touch() {
	p.lastHW.Store(time.Now().UnixNano())
	p.seeded.Store(false)
}

func (p *winProbe) hookLoop() {
	runtime.LockOSThread() // hooks are delivered to the installing thread's message loop
	// The OS passes the event struct as lParam; typed pointer params keep vet happy.
	next := func(code, w uintptr, l unsafe.Pointer) uintptr {
		r, _, _ := pCallNextHookEx.Call(0, code, w, uintptr(l))
		return r
	}
	kb := windows.NewCallback(func(code, w uintptr, ev *kbdLL) uintptr {
		if int32(code) >= 0 && ev.Flags&llkhfInjected == 0 {
			p.touch()
		}
		return next(code, w, unsafe.Pointer(ev))
	})
	ms := windows.NewCallback(func(code, w uintptr, ev *mouseLL) uintptr {
		if int32(code) >= 0 && ev.Flags&llmhfInjected == 0 {
			p.touch()
		}
		return next(code, w, unsafe.Pointer(ev))
	})

	mod, _, _ := pGetModuleHandle.Call(0)
	pSetWindowsHookEx.Call(whKeyboardLL, kb, mod, 0)
	pSetWindowsHookEx.Call(whMouseLL, ms, mod, 0)

	var m winMsg
	for {
		if r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0); int32(r) <= 0 {
			return
		}
	}
}

func (p *winProbe) Read() (Reading, error) {
	now := time.Now()
	r := Reading{At: now, Source: SourceHID, Locked: locked(), Remote: remote()}
	if p.seeded.Load() {
		r.Source = SourceOS
	}
	if last := p.lastHW.Load(); last != 0 {
		r.Idle = now.Sub(time.Unix(0, last))
	} else {
		r.Source = SourceNone
	}
	if ev, ok := fullscreen(); ok {
		r.Evidence = append(r.Evidence, ev)
	}
	return r, nil
}

func (p *winProbe) Close() error { return nil }

func osIdle() (time.Duration, bool) {
	info := struct{ Size, Time uint32 }{Size: 8}
	if r, _, _ := pGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0, false
	}
	now, _, _ := pGetTickCount.Call()
	return time.Duration(uint32(now)-info.Time) * time.Millisecond, true // uint32 math handles wraparound
}

// locked: the input desktop is the secure desktop (lock screen, UAC) or
// unreachable from this session.
func locked() bool {
	h, _, _ := pOpenInputDesktop.Call(0, 0, desktopSwitch)
	if h == 0 {
		return true
	}
	defer pCloseDesktop.Call(h)
	var name [64]uint16
	var n uint32
	pGetUserObjectInfo.Call(h, uoiName, uintptr(unsafe.Pointer(&name[0])), unsafe.Sizeof(name), uintptr(unsafe.Pointer(&n)))
	return !strings.EqualFold(windows.UTF16ToString(name[:]), "Default")
}

func remote() bool {
	r, _, _ := pGetSystemMetrics.Call(smRemoteSession)
	return r != 0
}

func fullscreen() (Evidence, bool) {
	var state uint32
	if hr, _, _ := pQueryNotification.Call(uintptr(unsafe.Pointer(&state))); hr != 0 {
		return Evidence{}, false
	}
	detail := map[uint32]string{qunsBusy: "fullscreen", qunsD3DFull: "d3d", qunsPresentation: "presentation"}[state]
	if detail == "" {
		return Evidence{}, false
	}
	var pid uint32
	if hwnd, _, _ := pGetForegroundWindow.Call(); hwnd != 0 {
		pGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	}
	return Evidence{Kind: KindFullscreen, PID: int(pid), Detail: detail}, true
}
