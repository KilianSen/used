package main

import "golang.org/x/sys/windows"

var (
	pFreeConsole      = windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole")
	pGetConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	pShowWindow       = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow")
)

// background hides and drops the console window when started from autostart.
func background() {
	if hwnd, _, _ := pGetConsoleWindow.Call(); hwnd != 0 {
		pShowWindow.Call(hwnd, 0 /* SW_HIDE */)
	}
	pFreeConsole.Call()
}
