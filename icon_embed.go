package main

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

// The product icon, drawn by tools/iconfamily in the workspace root. Fyne takes a
// resource, so the embedded PNG is wrapped once and shared.
//
//go:embed assets/icon.png
var appIconPNG []byte

var appIcon = fyne.NewStaticResource("icon.png", appIconPNG)
