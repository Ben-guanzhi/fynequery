package main

// Regression tests for the header column-resize drag. A stale/short colWidths
// slice under frozen state used to index out of range (the app panicked on a
// header drag after the column set changed); Dragged must rebuild the slice to
// span every column before mutating it.

import (
	"testing"

	"fyne.io/fyne/v2"
)

func TestHeaderDragRebuildsStaleColWidths(t *testing.T) {
	cases := []struct {
		name      string
		colWidths []float32
		dragCol   int
	}{
		{"empty colWidths (the crash)", []float32{}, 0},
		{"short colWidths", []float32{120}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newDataTable(&TableData{
				Cols: []ColumnInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}},
			}, nil)
			// Crash precondition: frozen with a colWidths slice that does not
			// span the current columns.
			g.frozen = true
			g.colWidths = tc.colWidths

			h := &headerWidget{t: g, dragCol: tc.dragCol}
			// Must not panic; must rebuild colWidths to cover all columns.
			h.Dragged(&fyne.DragEvent{Dragged: fyne.Delta{DX: 15}})

			if len(g.colWidths) != 3 {
				t.Fatalf("colWidths should span all 3 columns, got %d", len(g.colWidths))
			}
			if g.colWidths[tc.dragCol] < minColWidth {
				t.Fatalf("resized width below min: %v", g.colWidths[tc.dragCol])
			}
		})
	}
}

func TestHeaderDragNoColumnsIsNoop(t *testing.T) {
	g := newDataTable(&TableData{}, nil) // no columns
	h := &headerWidget{t: g, dragCol: -1}
	// columnAtX resolves to -1 with no columns; the drag must be dropped.
	h.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.Position{X: 10}}, Dragged: fyne.Delta{DX: 15}})
	if h.dragCol != -1 {
		t.Fatalf("dragCol should stay -1 with no columns, got %d", h.dragCol)
	}
}
