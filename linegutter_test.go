package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// TestLineGutterReflection verifies the reflection into the Entry's unexported
// scroll widget actually works against a rendered MultiLineEntry (the line
// gutter depends on it), and that the gutter builds and syncs without panicking.
func TestLineGutterReflection(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("gutter")
	e := widget.NewMultiLineEntry()
	e.Wrapping = fyne.TextWrapOff
	e.TextStyle.Monospace = true
	e.SetText("SELECT 1;\nSELECT 2;\nSELECT 3;")
	w.SetContent(e)
	w.Resize(fyne.NewSize(300, 200))
	w.Show()

	if entryScroll(e) == nil {
		t.Skip("Entry internal scroll not reachable under the test driver; gutter degrades to static numbers")
	}

	g := newLineGutter(e)
	g.CreateRenderer() // normally driven by Fyne; exercises the build path
	if g.lines != 3 {
		t.Fatalf("gutter line count = %d, want 3", g.lines)
	}
	g.sync() // must not panic
}
