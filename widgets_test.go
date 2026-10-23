package main

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

// TestAccentSplitResize verifies the draggable split used for the resizable
// sidebar and detail panel sizes its panes from the offset and collapses
// cleanly when a pane is hidden.
func TestAccentSplitResize(t *testing.T) {
	test.NewApp()
	leading := canvas.NewRectangle(color.NRGBA{R: 255, A: 255})
	trailing := canvas.NewRectangle(color.NRGBA{B: 255, A: 255})

	s := newAccentSplit(leading, trailing)
	w := test.NewWindow(s)
	defer w.Close()
	w.Resize(fyne.NewSize(1000, 400))

	s.SetOffset(0.25)
	s.Resize(fyne.NewSize(1000, 400))

	wantLeading := float32(0.25 * (1000 - accentSplitThickness))
	if got := leading.Size().Width; got < wantLeading-1 || got > wantLeading+1 {
		t.Fatalf("leading width = %v, want ~%v", got, wantLeading)
	}
	wantTrailing := 1000 - accentSplitThickness - wantLeading
	if got := trailing.Size().Width; got < wantTrailing-1 || got > wantTrailing+1 {
		t.Fatalf("trailing width = %v, want ~%v", got, wantTrailing)
	}

	// Hiding the leading pane hands the whole width to the trailing pane.
	leading.Hide()
	s.Refresh()
	if got := trailing.Size().Width; got != 1000 {
		t.Fatalf("trailing width after hide = %v, want 1000", got)
	}
}

// TestSideModeButtonStableWidth guards the tab-shift regression: the active
// state toggles bold, which is wider, so the button must reserve the bold width
// up front — otherwise switching tabs re-lays-out the row and the sibling
// labels visibly shift.
func TestSideModeButtonStableWidth(t *testing.T) {
	test.NewApp()
	for _, label := range []string{"JSON Tree", "Text", "Hex", "项目"} {
		b := newSideModeButton(label, nil)
		inactive := b.MinSize().Width
		b.setActive(true)
		if active := b.MinSize().Width; active != inactive {
			t.Fatalf("%q: active width %v != inactive %v — labels would shift on switch", label, active, inactive)
		}
	}
}
