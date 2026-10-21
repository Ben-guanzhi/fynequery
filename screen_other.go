//go:build !windows

package main

// Non-Windows platforms: no work-area query, so callers keep their requested
// window size unclamped.

import "fyne.io/fyne/v2"

func screenWorkArea(float32) fyne.Size { return fyne.Size{} }
