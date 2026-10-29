//go:build windows

package tray

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows: a hidden top-level window owns a Shell_NotifyIcon entry and runs
// the message loop on a locked OS thread. Clicks pop up a native menu.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassEx     = user32.NewProc("RegisterClassExW")
	pCreateWindowEx      = user32.NewProc("CreateWindowExW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pDefWindowProc       = user32.NewProc("DefWindowProcW")
	pGetMessage          = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessage     = user32.NewProc("DispatchMessageW")
	pPostMessage         = user32.NewProc("PostMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pRegisterWindowMsg   = user32.NewProc("RegisterWindowMessageW")
	pCreateIconFromRes   = user32.NewProc("CreateIconFromResourceEx")
	pDestroyIcon         = user32.NewProc("DestroyIcon")
	pCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	pAppendMenu          = user32.NewProc("AppendMenuW")
	pTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	pDestroyMenu         = user32.NewProc("DestroyMenu")
	pGetCursorPos        = user32.NewProc("GetCursorPos")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pShellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmNull      = 0x0000
	wmDestroy   = 0x0002
	wmLButtonUp = 0x0202
	wmRButtonUp = 0x0205
	wmApp       = 0x8000
	msgTray     = wmApp + 1 // icon callback
	msgUpdate   = wmApp + 2 // pending menu is ready
	msgQuit     = wmApp + 3 // ctx ended

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfSeparator = 0x0800

	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020
	tpmReturnCmd   = 0x0100

	lrDefaultColor = 0
	iconVersion    = 0x00030000
)

type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     uintptr
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type winMsg struct {
	HWnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

type point struct{ X, Y int32 }

// tray is the single live icon; the window procedure is global, so is this.
type tray struct {
	hwnd           uintptr
	icon           uintptr
	menu           Menu // UI thread only
	taskbarCreated uint32
	onClick        func(id string)

	mu      sync.Mutex
	pending *Menu
}

var (
	cur     *tray
	wndProc = sync.OnceValue(func() uintptr { return windows.NewCallback(proc) })
)

func run(ctx context.Context, first Menu, updates <-chan Menu, onClick func(id string)) error {
	t := &tray{menu: first, onClick: onClick}
	ready := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Never unlocked: the thread dies with the goroutine, taking its
		// message queue (and any stray WM_QUIT) with it.
		runtime.LockOSThread()
		if err := t.create(); err != nil {
			ready <- err
			return
		}
		ready <- nil
		var m winMsg
		for {
			r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 { // WM_QUIT or error
				return
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	if err := <-ready; err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			pPostMessage.Call(t.hwnd, msgQuit, 0, 0)
			<-done
			return nil
		case <-done: // window destroyed by the system
			return nil
		case m, ok := <-updates:
			if !ok {
				updates = nil
				continue
			}
			t.mu.Lock()
			t.pending = &m
			t.mu.Unlock()
			pPostMessage.Call(t.hwnd, msgUpdate, 0, 0)
		}
	}
}

// create registers the class, makes the hidden window and adds the icon.
func (t *tray) create() error {
	inst, _, _ := pGetModuleHandle.Call(0)
	class := windows.StringToUTF16Ptr("usedTrayWindow")
	wc := wndClassEx{WndProc: wndProc(), Instance: inst, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 &&
		!errors.Is(err, windows.ERROR_CLASS_ALREADY_EXISTS) {
		return fmt.Errorf("tray: RegisterClassEx: %w", err)
	}
	r, _, _ := pRegisterWindowMsg.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("TaskbarCreated"))))
	t.taskbarCreated = uint32(r)

	cur = t
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(class)),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("used"))), 0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("tray: CreateWindowEx: %w", err)
	}
	t.hwnd = hwnd
	if err := t.apply(nimAdd); err != nil {
		pDestroyWindow.Call(hwnd)
		return err
	}
	return nil
}

// apply pushes t.menu's icon and tooltip to the shell.
func (t *tray) apply(op uintptr) error {
	nid := notifyIconData{Wnd: t.hwnd, ID: 1, Flags: nifMessage | nifTip, CallbackMessage: msgTray}
	nid.Size = uint32(unsafe.Sizeof(nid))
	tip := utf16.Encode([]rune(strings.ReplaceAll(t.menu.Tooltip, "\x00", "")))
	copy(nid.Tip[:len(nid.Tip)-1], tip)

	old := t.icon
	if t.menu.Icon != nil {
		if png := PNG(t.menu.Icon); len(png) > 0 {
			h, _, _ := pCreateIconFromRes.Call(uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)),
				1, iconVersion, 0, 0, lrDefaultColor)
			if h != 0 {
				t.icon = h
			}
		}
	}
	if t.icon != 0 {
		nid.Flags |= nifIcon
		nid.Icon = t.icon
	}
	r, _, err := pShellNotifyIcon.Call(op, uintptr(unsafe.Pointer(&nid)))
	if old != 0 && old != t.icon {
		pDestroyIcon.Call(old)
	}
	if r == 0 {
		return fmt.Errorf("tray: Shell_NotifyIcon(%d): %w", op, err)
	}
	return nil
}

func (t *tray) remove() {
	nid := notifyIconData{Wnd: t.hwnd, ID: 1}
	nid.Size = uint32(unsafe.Sizeof(nid))
	pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	if t.icon != 0 {
		pDestroyIcon.Call(t.icon)
		t.icon = 0
	}
}

// showMenu pops up the current items at the cursor and dispatches a pick.
func (t *tray) showMenu() {
	items := t.menu.Items
	h, _, _ := pCreatePopupMenu.Call()
	if h == 0 {
		return
	}
	defer pDestroyMenu.Call(h)
	for i, it := range items {
		if it.Label == "" {
			pAppendMenu.Call(h, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if it.Disabled || it.ID == "" {
			flags |= mfGrayed
		}
		if it.Checked {
			flags |= mfChecked
		}
		// "&" marks a mnemonic; double it to show it literally.
		label := strings.ReplaceAll(strings.ReplaceAll(it.Label, "\x00", ""), "&", "&&")
		pAppendMenu.Call(h, flags, uintptr(i+1), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(label))))
	}
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(t.hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(h, tpmReturnCmd|tpmRightButton|tpmBottomAlign,
		uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	pPostMessage.Call(t.hwnd, wmNull, 0, 0) // lets the menu close properly
	if i := int(cmd) - 1; i >= 0 && i < len(items) && items[i].ID != "" && !items[i].Disabled {
		go t.onClick(items[i].ID)
	}
}

func proc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	t := cur
	if t == nil || hwnd != t.hwnd {
		r, _, _ := pDefWindowProc.Call(hwnd, uintptr(msg), wp, lp)
		return r
	}
	switch msg {
	case msgTray:
		switch lp & 0xFFFF {
		case wmLButtonUp, wmRButtonUp:
			t.showMenu()
		}
		return 0
	case msgUpdate:
		t.mu.Lock()
		p := t.pending
		t.pending = nil
		t.mu.Unlock()
		if p != nil {
			t.menu = *p
			_ = t.apply(nimModify)
		}
		return 0
	case msgQuit:
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		t.remove()
		pPostQuitMessage.Call(0)
		return 0
	}
	if t.taskbarCreated != 0 && msg == t.taskbarCreated {
		_ = t.apply(nimAdd) // explorer restarted
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, uintptr(msg), wp, lp)
	return r
}
