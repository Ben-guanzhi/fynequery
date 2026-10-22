//go:build !windows

package main

import "fyne.io/fyne/v2"

// The native caption stays on non-Windows platforms; the in-app title row
// still shows the toolbar, just without window buttons or caption dragging.

const chromeEnabled = false

func enableCustomChrome(fyne.Window) {}
func chromeFitWindow(fyne.Window, fyne.Size) bool { return false }
func chromeMinimize()                {}
func chromeToggleMax()               {}
func chromeStartDrag()               {}
