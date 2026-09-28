package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// searchResult is a fixed grid: "li" appears in exactly three cells.
func searchResult() db.QueryResult {
	return db.QueryResult{
		Columns:  []string{"name", "city"},
		ColTypes: []string{"text", "text"},
		Rows: [][]interface{}{
			{"Alice", "Shanghai"},
			{"Bob", "Berlin"},
			{"Carol", "Lima"},
		},
		RowCount: 3,
	}
}

// TestGridSearch covers the whole result-search loop: term → hits → Enter
// steps forward → Shift+Enter steps back (wrapping) → highlights → reset by a
// new result.
func TestGridSearch(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildQueryView())
	c.renderResult(searchResult())

	// The box sits in the cell-bar HBox, which lays children at their MinSize:
	// a bare Entry would be ~37px wide, i.e. unusable.
	if mw := c.find.entry.MinSize().Width; mw < 150 {
		t.Fatalf("search box min width = %.0f, want >= 150", mw)
	}

	c.find.entry.SetText("li")
	if len(c.find.hits) != 3 {
		t.Fatalf("hits = %d, want 3 (%v)", len(c.find.hits), c.find.hits)
	}
	// Reading order: Alice(0,0), Berlin(1,1), Lima(2,1).
	wantHits := []searchHitKey{{0, 0}, {1, 1}, {2, 1}}
	for i, h := range wantHits {
		if c.find.hits[i] != h {
			t.Fatalf("hits = %v, want %v", c.find.hits, wantHits)
		}
	}
	if got := c.find.label.Text; got != "1/3" {
		t.Fatalf("counter = %q, want 1/3", got)
	}
	if !c.isSearchHit(1, 1) || c.isSearchHit(1, 0) {
		t.Fatal("highlight set wrong")
	}
	// The first hit is revealed through the table selection.
	if !c.grid.hasCell || c.grid.cell != (widget.TableCellID{Row: 0, Col: 0}) {
		t.Fatalf("selection = %v (hasCell %v), want {0 0}", c.grid.cell, c.grid.hasCell)
	}

	// Enter → next hit.
	c.find.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if c.find.cur != 1 || c.find.label.Text != "2/3" {
		t.Fatalf("after Enter: cur=%d counter=%q, want cur=1 2/3", c.find.cur, c.find.label.Text)
	}
	if c.grid.view.Text != "Berlin" {
		t.Fatalf("cell viewer = %q, want Berlin", c.grid.view.Text)
	}

	// Shift+Enter → back, then wrapping to the last hit.
	c.find.entry.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	c.find.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	c.find.entry.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	if c.find.cur != 0 || c.find.label.Text != "1/3" {
		t.Fatalf("after Shift+Enter: cur=%d counter=%q, want cur=0 1/3", c.find.cur, c.find.label.Text)
	}
	c.find.entry.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftRight})
	c.find.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEnter})
	c.find.entry.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftRight})
	if c.find.cur != 2 || c.find.label.Text != "3/3" {
		t.Fatalf("after wrapping Shift+Enter: cur=%d counter=%q, want cur=2 3/3", c.find.cur, c.find.label.Text)
	}
	// Plain Enter must still insert nothing into the box.
	if c.find.entry.Text != "li" {
		t.Fatalf("search text = %q, want unchanged %q", c.find.entry.Text, "li")
	}

	// Emptying the box clears hits, highlights and counter.
	c.find.entry.SetText("")
	if len(c.find.hits) != 0 || c.isSearchHit(1, 1) || c.find.label.Text != "" {
		t.Fatalf("clear failed: hits=%v label=%q", c.find.hits, c.find.label.Text)
	}

	// A sort reorders rows, so the hits must be re-collected: Lima moves from
	// row 2 to row 1 under ascending city order.
	c.find.entry.SetText("lima")
	if len(c.find.hits) != 1 || c.find.hits[0] != (searchHitKey{2, 1}) {
		t.Fatalf("hits = %v, want [{2 1}]", c.find.hits)
	}
	c.sortGrid(1)
	if len(c.find.hits) != 1 || c.find.hits[0] != (searchHitKey{1, 1}) {
		t.Fatalf("hits after sort = %v, want [{1 1}]", c.find.hits)
	}

	// A new result resets the search entirely.
	c.find.entry.SetText("li")
	c.renderResult(searchResult())
	if c.find.term != "" || len(c.find.hits) != 0 || c.find.entry.Text != "" {
		t.Fatalf("new result did not reset search: term=%q hits=%v entry=%q",
			c.find.term, c.find.hits, c.find.entry.Text)
	}
}

// TestGridSearchNoHit checks a term that matches nothing leaves the grid
// usable: no spotlight, zero counter, no panic.
func TestGridSearchNoHit(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildQueryView())
	c.renderResult(searchResult())

	c.find.entry.SetText("zzz")
	if len(c.find.hits) != 0 {
		t.Fatalf("hits = %v, want none", c.find.hits)
	}
	if got := c.find.label.Text; got != "0/0" {
		t.Fatalf("counter = %q, want 0/0", got)
	}
	c.find.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn}) // must not panic
}
