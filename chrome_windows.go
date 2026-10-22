//go:build windows

package main

// Custom window chrome: the native caption is removed (client area extended
// over the whole window) so the in-app title row — window title, toolbar and
// window buttons — occupies the space the OS caption used to take, the way
// TablePro merges its toolbar into the title bar.

import (
	"math"
	"syscall"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
)

var (
	chromeUser32          = syscall.NewLazyDLL("user32.dll")
	procGetWindowLongPtrW = chromeUser32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW = chromeUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = chromeUser32.NewProc("CallWindowProcW")
	procSetWindowPos      = chromeUser32.NewProc("SetWindowPos")
	procSendMessageW      = chromeUser32.NewProc("SendMessageW")
	procReleaseCapture    = chromeUser32.NewProc("ReleaseCapture")
	procIsZoomed          = chromeUser32.NewProc("IsZoomed")
	procMonitorFromWindow = chromeUser32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW   = chromeUser32.NewProc("GetMonitorInfoW")
	procGetClientRect     = chromeUser32.NewProc("GetClientRect")
	procGetDpiForWindow   = chromeUser32.NewProc("GetDpiForWindow")
	procAdjustWindowRect  = chromeUser32.NewProc("AdjustWindowRectExForDpi")
)

const (
	gwlpWndProc           = ^uintptr(3)  // GWLP_WNDPROC = -4
	gwlStyle              = ^uintptr(15) // GWL_STYLE = -16
	gwlExStyle            = ^uintptr(19) // GWL_EXSTYLE = -20
	wmGetMinMaxInfo       = 0x0024
	wmWindowPosChanging   = 0x0046
	wmNCCalcSize          = 0x0083
	wmNCLButtonDown       = 0x00A1
	wmSysCommand          = 0x0112
	htCaption             = 0x0002
	scMinimize            = 0xF020
	scMaximize            = 0xF030
	scRestore             = 0xF120
	monitorDefaultNearest = 0x0002
	swpNoSize             = 0x0001
	swpNoMove             = 0x0002
	swpNoZOrder           = 0x0004
	swpNoActivate         = 0x0010
	swpFrameChanged       = 0x0020
)

const chromeEnabled = true

var (
	chromeHWND     uintptr
	chromePrevProc uintptr
	chromeActive   bool
	// held in a global so the GC never frees the native callback
	chromeCallback = syscall.NewCallback(chromeWndProc)
)

type chromeMinMaxInfo struct {
	ptReserved, ptMaxSize, ptMaxPosition, ptMaxTrackSize, ptMinTrackSize [2]int32
}

type chromeWindowPos struct {
	hwnd, insertAfter uintptr
	x, y, cx, cy      int32
	flags             uint32
}

type chromeMonitorInfo struct {
	cbSize    uint32
	rcMonitor sysRect
	rcWork    sysRect
	dwFlags   uint32
}

func chromeWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmWindowPosChanging:
		chromeDropStaleFrame(hwnd, lp)
	case wmNCCalcSize:
		if wp != 0 {
			return 0 // client area = full window rect, caption gone
		}
	case wmGetMinMaxInfo:
		// Keep a maximised window inside the monitor work area: with no
		// non-client frame the default calculation would cover the taskbar.
		mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultNearest)
		if mon == 0 {
			break
		}
		var mi chromeMonitorInfo
		mi.cbSize = uint32(unsafe.Sizeof(mi))
		if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
			break
		}
		mm := (*chromeMinMaxInfo)(unsafe.Pointer(lp)) //nolint: // Win32 message params are raw pointers; the uintptr→Pointer conversion is mandated by the ABI
		mm.ptMaxPosition[0] = mi.rcWork.Left - mi.rcMonitor.Left
		mm.ptMaxPosition[1] = mi.rcWork.Top - mi.rcMonitor.Top
		mm.ptMaxSize[0] = mi.rcWork.Right - mi.rcWork.Left
		mm.ptMaxSize[1] = mi.rcWork.Bottom - mi.rcWork.Top
		return 0
	}
	ret, _, _ := procCallWindowProcW.Call(chromePrevProc, hwnd, msg, wp, lp)
	return ret
}

// chromeDropStaleFrame cancels the caption inflation GLFW adds to a resize
// that keeps the current client size. GLFW's SetSize still believes the removed
// caption exists and pads the requested client size with its frame margins, so
// a no-op resize would grow the window by 18x47 px. Fyne issues exactly that
// when an overlay (e.g. the hover tooltip popup) is first added to the canvas,
// which made the window jump on hover.
func chromeDropStaleFrame(hwnd, lp uintptr) {
	if !chromeActive || hwnd != chromeHWND {
		return
	}
	pos := (*chromeWindowPos)(unsafe.Pointer(lp)) //nolint: // WINDOWPOS arrives as a raw pointer in lParam
	// Only reshape plain client-size requests like GLFW's SetSize; never
	// interfere with move+resize operations such as maximize, snap or drag.
	if pos.flags&(swpNoSize|swpNoMove|swpNoActivate) != swpNoMove|swpNoActivate {
		return
	}
	var cr sysRect
	if r, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr))); r == 0 {
		return
	}
	frameW, frameH := chromeFrameMargins(hwnd)
	cw, ch := cr.Right-cr.Left, cr.Bottom-cr.Top
	if pos.cx != cw+frameW || pos.cy != ch+frameH {
		return
	}
	pos.cx, pos.cy = cw, ch
}

// chromeFrameMargins returns the non-client size GLFW adds around a requested
// client size, queried with the same API and window style GLFW uses.
func chromeFrameMargins(hwnd uintptr) (int32, int32) {
	style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
	exStyle, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlExStyle)
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	var r sysRect
	if ok, _, _ := procAdjustWindowRect.Call(uintptr(unsafe.Pointer(&r)), style, 0, exStyle, dpi); ok == 0 {
		return 0, 0
	}
	return r.Right - r.Left, r.Bottom - r.Top
}

// enableCustomChrome strips the native caption from w so the in-app title row
// becomes the window's top row. Must run after the window is shown.
func enableCustomChrome(w fyne.Window) {
	nw, ok := w.(driver.NativeWindow)
	if !ok {
		return
	}
	nw.RunNative(func(ctx any) {
		// Fyne passes the context by value, not by pointer.
		wc, ok := ctx.(driver.WindowsWindowContext)
		if !ok || wc.HWND == 0 {
			return
		}
		chromeHWND = wc.HWND
		// Sample the canvas before stripping the caption: removing it grows
		// the client over the old frame and the resulting WM_SIZE inflates the
		// canvas to the old outer size. The pre-strip size is the one the
		// window should keep.
		target := w.Canvas().Size()
		scale := w.Canvas().Scale()
		prev, _, _ := procGetWindowLongPtrW.Call(chromeHWND, gwlpWndProc)
		chromePrevProc = prev
		procSetWindowLongPtrW.Call(chromeHWND, gwlpWndProc, chromeCallback)
		procSetWindowPos.Call(chromeHWND, 0, 0, 0, 0, 0,
			swpNoMove|swpNoSize|swpNoZOrder|swpFrameChanged)
		// Removing the caption makes the client area grow over the old frame,
		// but GLFW still sizes the window as if that frame existed: a later
		// Fyne Resize re-creates the same outer rect while its canvas shrinks
		// back. That leaves an unpainted band at the top and shifts every
		// pointer hit. With no non-client frame the window and client rects
		// are the same, so size the window to the pre-strip canvas directly.
		chromePlaceWindow(target, scale)
		chromeActive = true
	})
}

// chromeFitWindow resizes and centres w natively when it is the chromed
// window, bypassing Fyne's resize (which re-adds the removed caption margins).
// It returns false when w is not chromed, so callers can use the normal path.
func chromeFitWindow(w fyne.Window, size fyne.Size) bool {
	nw, ok := w.(driver.NativeWindow)
	if !ok {
		return false
	}
	handled := false
	nw.RunNative(func(ctx any) {
		wc, ok := ctx.(driver.WindowsWindowContext)
		if !ok || wc.HWND == 0 || wc.HWND != chromeHWND {
			return
		}
		chromePlaceWindow(size, w.Canvas().Scale())
		handled = true
	})
	return handled
}

// chromePlaceWindow sets the native window to size in screen pixels and
// centres it in the monitor work area. The caption is gone, so the width and
// height passed to SetWindowPos describe the client area directly.
func chromePlaceWindow(size fyne.Size, scale float32) {
	if chromeHWND == 0 {
		return
	}
	width := int(math.Ceil(float64(size.Width * scale)))
	height := int(math.Ceil(float64(size.Height * scale)))
	if width <= 0 || height <= 0 {
		return
	}
	flags := uintptr(swpNoMove | swpNoZOrder | swpNoActivate)
	if mon, _, _ := procMonitorFromWindow.Call(chromeHWND, monitorDefaultNearest); mon != 0 {
		var mi chromeMonitorInfo
		mi.cbSize = uint32(unsafe.Sizeof(mi))
		if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r != 0 {
			workW := int(mi.rcWork.Right - mi.rcWork.Left)
			workH := int(mi.rcWork.Bottom - mi.rcWork.Top)
			x := int(mi.rcWork.Left) + (workW-width)/2
			y := int(mi.rcWork.Top) + (workH-height)/2
			flags = swpNoZOrder | swpNoActivate
			procSetWindowPos.Call(chromeHWND, 0, uintptr(x), uintptr(y),
				uintptr(width), uintptr(height), flags)
			return
		}
	}
	procSetWindowPos.Call(chromeHWND, 0, 0, 0, uintptr(width), uintptr(height), flags)
}

func chromeMinimize() {
	if chromeHWND != 0 {
		procSendMessageW.Call(chromeHWND, wmSysCommand, scMinimize, 0)
	}
}

func chromeToggleMax() {
	if chromeHWND == 0 {
		return
	}
	cmd := uintptr(scMaximize)
	if r, _, _ := procIsZoomed.Call(chromeHWND); r != 0 {
		cmd = scRestore
	}
	procSendMessageW.Call(chromeHWND, wmSysCommand, cmd, 0)
}

// chromeStartDrag hands pointer movement to the OS caption dragger; the modal
// move loop runs until the button is released.
func chromeStartDrag() {
	if chromeHWND == 0 {
		return
	}
	procReleaseCapture.Call()
	procSendMessageW.Call(chromeHWND, wmNCLButtonDown, htCaption, 0)
}
