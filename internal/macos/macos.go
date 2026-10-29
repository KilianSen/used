//go:build darwin

// Package macos is a small cgo-free bridge (via purego) to the macOS
// frameworks used needs: CoreFoundation helpers plus framework handles.
package macos

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// Framework handles, valid after Load.
var CoreGraphics, IOKit, CoreFoundation, CoreAudio uintptr

var (
	once    sync.Once
	loadErr error

	createString  func(alloc uintptr, s string, enc uint32) uintptr
	release       func(ref uintptr)
	dictGet       func(d, key uintptr) uintptr
	dictCount     func(d uintptr) int
	dictKeysVals  func(d uintptr, keys, vals *uintptr)
	boolValue     func(b uintptr) bool
	numberValue   func(n uintptr, typ int, out *int64) bool
	arrayCount    func(a uintptr) int
	arrayAt       func(a uintptr, i int) uintptr
	stringToBytes func(s uintptr, buf *byte, size int, enc uint32) bool
	typeID        func(ref uintptr) uintptr
	stringTypeID  uintptr
)

const (
	utf8   = 0x08000100 // kCFStringEncodingUTF8
	sInt64 = 4          // kCFNumberSInt64Type
	fwRoot = "/System/Library/Frameworks/"
)

// Load opens the frameworks once. Safe to call repeatedly.
func Load() error {
	once.Do(func() {
		for _, fw := range []struct {
			dst  *uintptr
			name string
		}{{&CoreGraphics, "CoreGraphics"}, {&IOKit, "IOKit"}, {&CoreFoundation, "CoreFoundation"}, {&CoreAudio, "CoreAudio"}} {
			h, err := purego.Dlopen(fwRoot+fw.name+".framework/"+fw.name, purego.RTLD_LAZY|purego.RTLD_GLOBAL)
			if err != nil {
				loadErr = fmt.Errorf("load %s: %w", fw.name, err)
				return
			}
			*fw.dst = h
		}
		reg := func(fn any, name string) { purego.RegisterLibFunc(fn, CoreFoundation, name) }
		reg(&createString, "CFStringCreateWithCString")
		reg(&release, "CFRelease")
		reg(&dictGet, "CFDictionaryGetValue")
		reg(&dictCount, "CFDictionaryGetCount")
		reg(&dictKeysVals, "CFDictionaryGetKeysAndValues")
		reg(&boolValue, "CFBooleanGetValue")
		reg(&numberValue, "CFNumberGetValue")
		reg(&arrayCount, "CFArrayGetCount")
		reg(&arrayAt, "CFArrayGetValueAtIndex")
		reg(&stringToBytes, "CFStringGetCString")
		reg(&typeID, "CFGetTypeID")
		var strType func() uintptr
		reg(&strType, "CFStringGetTypeID")
		stringTypeID = strType()
	})
	return loadErr
}

// Func binds a C function from lib to fn (a pointer to a Go func variable).
func Func(fn any, lib uintptr, name string) { purego.RegisterLibFunc(fn, lib, name) }

// Str makes a CFString that lives for the life of the process (for keys).
func Str(s string) uintptr { return createString(0, s, utf8) }

func Release(ref uintptr) {
	if ref != 0 {
		release(ref)
	}
}

func Get(d, key uintptr) uintptr  { return dictGet(d, key) }
func Bool(b uintptr) bool         { return b != 0 && boolValue(b) }
func Len(a uintptr) int           { return arrayCount(a) }
func At(a uintptr, i int) uintptr { return arrayAt(a, i) }

func Int(n uintptr) int64 {
	var v int64
	if n != 0 {
		numberValue(n, sInt64, &v)
	}
	return v
}

// String converts a CFString; anything else (or NULL) yields "".
func String(s uintptr) string {
	if s == 0 || typeID(s) != stringTypeID {
		return ""
	}
	buf := make([]byte, 256)
	if !stringToBytes(s, &buf[0], len(buf), utf8) {
		return ""
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

// Each calls fn for every key/value of a CFDictionary.
func Each(d uintptr, fn func(k, v uintptr)) {
	n := dictCount(d)
	if n == 0 {
		return
	}
	keys, vals := make([]uintptr, n), make([]uintptr, n)
	dictKeysVals(d, &keys[0], &vals[0])
	for i := range n {
		fn(keys[i], vals[i])
	}
}
