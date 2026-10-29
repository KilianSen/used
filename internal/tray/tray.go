// Package tray shows a status icon with a menu. The model here is portable;
// each OS has one backend file implementing run.
//
//	macOS    NSStatusItem via purego/objc (no cgo)
//	Windows  Shell_NotifyIcon + popup menu
//	Linux    StatusNotifierItem + com.canonical.dbusmenu over D-Bus
package tray

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Menu is everything the tray shows. Backends render it as a whole.
type Menu struct {
	Icon    image.Image
	Tooltip string
	Items   []Item
}

// Item is one menu row. An empty Label is a separator.
type Item struct {
	ID       string // passed to the click handler; empty = not clickable
	Label    string
	Checked  bool
	Disabled bool
}

// Run shows the tray until ctx ends or the user quits through the OS.
// It must be called from the main goroutine (macOS needs the main thread).
// Menus received on updates replace the current one; clicks go to onClick
// (called on a backend goroutine, never blocking the UI for long is the
// caller's job).
func Run(ctx context.Context, first Menu, updates <-chan Menu, onClick func(id string)) error {
	return run(ctx, first, updates, onClick)
}

// Dot draws a filled circle of colour c on a transparent square, with a
// thin darker rim so it reads on light and dark bars. ring adds an outer
// ring (used to show running workloads).
func Dot(size int, c color.RGBA, ring bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	mid := float64(size) / 2
	inner := mid * 0.55
	if !ring {
		inner = mid * 0.8
	}
	rim := color.RGBA{c.R / 2, c.G / 2, c.B / 2, 255}
	for y := range size {
		for x := range size {
			d := math.Hypot(float64(x)+0.5-mid, float64(y)+0.5-mid)
			switch {
			case d <= inner-1:
				img.SetRGBA(x, y, c)
			case d <= inner:
				img.SetRGBA(x, y, rim)
			case ring && d >= mid*0.78 && d <= mid*0.95:
				img.SetRGBA(x, y, c)
			}
		}
	}
	return img
}

// PNG encodes an icon for backends that take image files.
func PNG(img image.Image) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// ARGB32 returns the pixels as big-endian ARGB, as StatusNotifierItem wants.
func ARGB32(img image.Image) (w, h int, data []byte) {
	r := img.Bounds()
	data = make([]byte, 0, r.Dx()*r.Dy()*4)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			data = append(data, c.A, c.R, c.G, c.B)
		}
	}
	return r.Dx(), r.Dy(), data
}
