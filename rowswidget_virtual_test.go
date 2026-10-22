package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
)

func TestVisibleWindowEmpty(t *testing.T) {
	if f, l := visibleWindow(0, 400, 20, 0, 6); f != 0 || l != -1 {
		t.Fatalf("empty: got (%d,%d) want (0,-1)", f, l)
	}
	if f, l := visibleWindow(0, 400, 0, 100, 6); f != 0 || l != -1 {
		t.Fatalf("zero rowH: got (%d,%d) want (0,-1)", f, l)
	}
}

func TestVisibleWindowTop(t *testing.T) {
	// viewport 400px, row 20px -> 20 visible rows, buffer 6 -> pool 32.
	f, l := visibleWindow(0, 400, 20, 100, 6)
	if f != 0 {
		t.Fatalf("first at top: got %d want 0", f)
	}
	if l != 31 {
		t.Fatalf("last at top: got %d want 31 (pool of 32)", l)
	}
}

func TestVisibleWindowScrolled(t *testing.T) {
	// offset 1000 (row 50), pool 32 -> first = 50-6 = 44, last = 75.
	f, l := visibleWindow(1000, 400, 20, 100, 6)
	if f != 44 {
		t.Fatalf("first scrolled: got %d want 44", f)
	}
	if l != 75 {
		t.Fatalf("last scrolled: got %d want 75", l)
	}
}

func TestVisibleWindowBufferClampTop(t *testing.T) {
	// Near the top: first must not go negative.
	f, _ := visibleWindow(10, 400, 20, 100, 6)
	if f != 0 {
		t.Fatalf("first clamped: got %d want 0", f)
	}
}

func TestVisibleWindowBottomClamp(t *testing.T) {
	// Large offset pushes last to the end of the data set.
	f, l := visibleWindow(100000, 400, 20, 100, 6)
	if l != 99 {
		t.Fatalf("last clamped: got %d want 99", l)
	}
	if f > l {
		t.Fatalf("first %d > last %d", f, l)
	}
}

func TestVisibleWindowSmallerThanPool(t *testing.T) {
	// Fewer rows than the pool size: window must shrink to the data set.
	f, l := visibleWindow(0, 400, 20, 5, 6)
	if f != 0 || l != 4 {
		t.Fatalf("small set: got (%d,%d) want (0,4)", f, l)
	}
}

// TestExtentWidgetReportsFullScrollHeight guards the wheel-scroll fix: the
// scroll content's MinSize must span the whole result set, otherwise the
// virtualized grid has no vertical range and the wheel does nothing.
func TestExtentWidgetReportsFullScrollHeight(t *testing.T) {
	e := newExtentWidget()
	e.setSize(320, 5600)
	if got := e.MinSize(); got != (fyne.Size{Width: 320, Height: 5600}) {
		t.Fatalf("extent MinSize = %v, want (320,5600)", got)
	}
	// A no-layout container takes the max of its children's MinSize, so the
	// extent must be what makes the content tall enough to scroll.
	content := container.NewWithoutLayout(e)
	if got := content.MinSize().Height; got != 5600 {
		t.Fatalf("content MinSize height = %v, want 5600", got)
	}
}
