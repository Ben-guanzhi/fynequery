//go:build windows

package main

// Windows monitor work area (the screen minus the taskbar), used to keep the
// main windows inside the visible desktop on scaled displays.

import (
	"syscall"
	"unsafe"

	"fyne.io/fyne/v2"
)

var (
	procSystemParametersInfo = syscall.NewLazyDLL("user32.dll").NewProc("SystemParametersInfoW")
)

const spiGetWorkArea = 0x0030

type sysRect struct{ Left, Top, Right, Bottom int32 }

// screenWorkArea returns the primary monitor work area in Fyne units for the
// given canvas scale. It returns a zero size when the query fails.
func screenWorkArea(scale float32) fyne.Size {
	var r sysRect
	ret, _, _ := procSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	if ret == 0 || r.Right <= r.Left || r.Bottom <= r.Top {
		return fyne.Size{}
	}
	if scale <= 0 {
		scale = 1
	}
	return fyne.NewSize(float32(r.Right-r.Left)/scale, float32(r.Bottom-r.Top)/scale)
}
