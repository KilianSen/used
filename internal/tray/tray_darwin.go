package tray

import (
	"context"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// AppKit must run on the main thread, which only the main goroutine can
// own. Lock it before main starts.
func init() { runtime.LockOSThread() }

var sel = objc.RegisterName

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

type nsSize struct{ W, H float64 }

// macTray holds what the main-thread methods need. Methods registered with
// the Objective-C runtime can't capture, so there is one package instance.
var mac struct {
	mu       sync.Mutex
	pending  Menu   // next menu to show, set from any goroutine
	shown    []Item // items currently in the NSMenu, indexed by tag
	onClick  func(string)
	delegate objc.ID // instance of UsedTray, target of menu items
	item     objc.ID // NSStatusItem
}

func run(ctx context.Context, first Menu, updates <-chan Menu, onClick func(string)) error {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		return err
	}
	cls, err := objc.RegisterClass("UsedTray", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel("apply:"), Fn: apply},
		{Cmd: sel("click:"), Fn: click},
		{Cmd: sel("quit:"), Fn: func(objc.ID, objc.SEL, objc.ID) {
			class("NSApplication").Send(sel("sharedApplication")).Send(sel("terminate:"), objc.ID(0))
		}},
	})
	if err != nil {
		return err
	}
	mac.onClick = onClick
	mac.delegate = objc.ID(cls).Send(sel("new"))

	app := class("NSApplication").Send(sel("sharedApplication"))
	app.Send(sel("setActivationPolicy:"), 1) // accessory: menu bar only, no Dock icon

	set(first)
	go func() {
		for {
			select {
			case <-ctx.Done():
				// terminate: exits the process, so Run does not return on macOS.
				mac.delegate.Send(sel("performSelectorOnMainThread:withObject:waitUntilDone:"), sel("quit:"), objc.ID(0), false)
				return
			case m, ok := <-updates:
				if !ok {
					updates = nil
					continue
				}
				set(m)
			}
		}
	}()
	app.Send(sel("run"))
	return nil
}

// set queues m for the main thread.
func set(m Menu) {
	mac.mu.Lock()
	mac.pending = m
	mac.mu.Unlock()
	mac.delegate.Send(sel("performSelectorOnMainThread:withObject:waitUntilDone:"), sel("apply:"), objc.ID(0), false)
}

// apply runs on the main thread and rebuilds icon, tooltip and menu.
func apply(objc.ID, objc.SEL, objc.ID) {
	mac.mu.Lock()
	m := mac.pending
	mac.shown = m.Items
	mac.mu.Unlock()

	if mac.item == 0 {
		bar := class("NSStatusBar").Send(sel("systemStatusBar"))
		mac.item = bar.Send(sel("statusItemWithLength:"), float64(-1)) // NSVariableStatusItemLength
		mac.item.Send(sel("retain"))
	}
	button := mac.item.Send(sel("button"))
	if m.Icon != nil {
		b := PNG(m.Icon)
		data := class("NSData").Send(sel("dataWithBytes:length:"), unsafe.Pointer(&b[0]), len(b))
		img := class("NSImage").Send(sel("alloc")).Send(sel("initWithData:"), data)
		img.Send(sel("setSize:"), nsSize{18, 18})
		button.Send(sel("setImage:"), img)
		img.Send(sel("release"))
		runtime.KeepAlive(b)
	}
	button.Send(sel("setToolTip:"), str(m.Tooltip))

	menu := class("NSMenu").Send(sel("alloc")).Send(sel("init"))
	menu.Send(sel("setAutoenablesItems:"), false)
	for i, it := range m.Items {
		if it.Label == "" {
			menu.Send(sel("addItem:"), class("NSMenuItem").Send(sel("separatorItem")))
			continue
		}
		var action objc.SEL
		if it.ID != "" {
			action = sel("click:")
		}
		mi := class("NSMenuItem").Send(sel("alloc")).Send(sel("initWithTitle:action:keyEquivalent:"), str(it.Label), action, str(""))
		mi.Send(sel("setTarget:"), mac.delegate)
		mi.Send(sel("setTag:"), i)
		mi.Send(sel("setEnabled:"), it.ID != "" && !it.Disabled)
		if it.Checked {
			mi.Send(sel("setState:"), 1)
		}
		menu.Send(sel("addItem:"), mi)
		mi.Send(sel("release"))
	}
	mac.item.Send(sel("setMenu:"), menu)
	menu.Send(sel("release"))
}

// click runs on the main thread when a menu item is chosen.
func click(_ objc.ID, _ objc.SEL, sender objc.ID) {
	i := objc.Send[int](sender, sel("tag"))
	mac.mu.Lock()
	var id string
	if i >= 0 && i < len(mac.shown) {
		id = mac.shown[i].ID
	}
	mac.mu.Unlock()
	if id != "" {
		go mac.onClick(id)
	}
}

func str(s string) objc.ID { return class("NSString").Send(sel("stringWithUTF8String:"), s) }
