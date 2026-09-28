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
	if mw := c.gridSearchEntry.MinSize().Width; mw < 150 {
		t.Fatalf("search box min width = %.0f, want >= 150", mw)
	}

	c.gridSearchEntry.SetText("li")
	if len(c.searchHits) != 3 {
		t.Fatalf("hits = %d, want 3 (%v)", len(c.searchHits), c.searchHits)
	}
	// Reading order: Alice(0,0), Berlin(1,1), Lima(2,1).
	wantHits := []searchHitKey{{0, 0}, {1, 1}, {2, 1}}
	for i, h := range wantHits {
		if c.searchHits[i] != h {
			t.Fatalf("hits = %v, want %v", c.searchHits, wantHits)
		}
	}
	if got := c.searchHitsLabel.Text; got != "1/3" {
		t.Fatalf("counter = %q, want 1/3", got)
	}
	if !c.isSearchHit(1, 1) || c.isSearchHit(1, 0) {
		t.Fatal("highlight set wrong")
	}
	// The first hit is revealed through the table selection.
	if !c.hasCell || c.selectedCell != (widget.TableCellID{Row: 0, Col: 0}) {
		t.Fatalf("selection = %v (hasCell %v), want {0 0}", c.selectedCell, c.hasCell)
	}

	// Enter → next hit.
	c.gridSearchEntry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if c.searchCur != 1 || c.searchHitsLabel.Text != "2/3" {
		t.Fatalf("after Enter: cur=%d counter=%q, want cur=1 2/3", c.searchCur, c.searchHitsLabel.Text)
	}
	if c.cellView.Text != "Berlin" {
		t.Fatalf("cell viewer = %q, want Berlin", c.cellView.Text)
	}

	// Shift+Enter → back, then wrapping to the last hit.
	c.gridSearchEntry.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	c.gridSearchEntry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	c.gridSearchEntry.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	if c.searchCur != 0 || c.searchHitsLabel.Text != "1/3" {
		t.Fatalf("after Shift+Enter: cur=%d counter=%q, want cur=0 1/3", c.searchCur, c.searchHitsLabel.Text)
	}
	c.gridSearchEntry.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftRight})
	c.gridSearchEntry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEnter})
	c.gridSearchEntry.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftRight})
	if c.searchCur != 2 || c.searchHitsLabel.Text != "3/3" {
		t.Fatalf("after wrapping Shift+Enter: cur=%d counter=%q, want cur=2 3/3", c.searchCur, c.searchHitsLabel.Text)
	}
	// Plain Enter must still insert nothing into the box.
	if c.gridSearchEntry.Text != "li" {
		t.Fatalf("search text = %q, want unchanged %q", c.gridSearchEntry.Text, "li")
	}

	// Emptying the box clears hits, highlights and counter.
	c.gridSearchEntry.SetText("")
	if len(c.searchHits) != 0 || c.isSearchHit(1, 1) || c.searchHitsLabel.Text != "" {
		t.Fatalf("clear failed: hits=%v label=%q", c.searchHits, c.searchHitsLabel.Text)
	}

	// A sort reorders rows, so the hits must be re-collected: Lima moves from
	// row 2 to row 1 under ascending city order.
	c.gridSearchEntry.SetText("lima")
	if len(c.searchHits) != 1 || c.searchHits[0] != (searchHitKey{2, 1}) {
		t.Fatalf("hits = %v, want [{2 1}]", c.searchHits)
	}
	c.sortGrid(1)
	if len(c.searchHits) != 1 || c.searchHits[0] != (searchHitKey{1, 1}) {
		t.Fatalf("hits after sort = %v, want [{1 1}]", c.searchHits)
	}

	// A new result resets the search entirely.
	c.gridSearchEntry.SetText("li")
	c.renderResult(searchResult())
	if c.searchTerm != "" || len(c.searchHits) != 0 || c.gridSearchEntry.Text != "" {
		t.Fatalf("new result did not reset search: term=%q hits=%v entry=%q",
			c.searchTerm, c.searchHits, c.gridSearchEntry.Text)
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

	c.gridSearchEntry.SetText("zzz")
	if len(c.searchHits) != 0 {
		t.Fatalf("hits = %v, want none", c.searchHits)
	}
	if got := c.searchHitsLabel.Text; got != "0/0" {
		t.Fatalf("counter = %q, want 0/0", got)
	}
	c.gridSearchEntry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn}) // must not panic
}
