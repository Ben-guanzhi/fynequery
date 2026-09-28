package main

import (
	"testing"

	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// TestRebuildHistoryView checks the history panel's filtered view: newest
// first, matched against the statement text, cleared by an empty filter.
func TestRebuildHistoryView(t *testing.T) {
	test.NewApp()
	a := test.NewApp()
	w := a.NewWindow("")
	c := newTestClient(a, w)
	c.store.ReplaceHistory([]db.HistoryEntry{
		{ID: 1, SQL: "SELECT * FROM users"},
		{ID: 2, SQL: "UPDATE orders SET a = 1", Table: "orders"},
		{ID: 3, SQL: "INSERT INTO users (name) VALUES ('x')"},
	})

	c.hist.filter = ""
	c.rebuildHistoryView()
	if len(c.hist.view) != 3 || c.hist.view[0].ID != 3 {
		t.Fatalf("unfiltered view = %+v, want 3 entries newest first", c.hist.view)
	}

	c.hist.filter = "users"
	c.rebuildHistoryView()
	if len(c.hist.view) != 2 || c.hist.view[0].ID != 3 || c.hist.view[1].ID != 1 {
		t.Fatalf("filtered view = %+v, want ids [3 1]", c.hist.view)
	}

	// The Table field is searched too.
	c.hist.filter = "orders"
	c.rebuildHistoryView()
	if len(c.hist.view) != 1 || c.hist.view[0].ID != 2 {
		t.Fatalf("table-filtered view = %+v, want id 2", c.hist.view)
	}

	// The database field is searched as well.
	c.hist.filter = "zs"
	entries := c.store.History()
	entries[1].Database = "zs"
	c.store.ReplaceHistory(entries)
	c.rebuildHistoryView()
	if len(c.hist.view) != 1 || c.hist.view[0].ID != 2 {
		t.Fatalf("db-filtered view = %+v, want id 2", c.hist.view)
	}

	c.hist.filter = "no such thing"
	c.rebuildHistoryView()
	if len(c.hist.view) != 0 {
		t.Fatalf("no-match view = %+v, want empty", c.hist.view)
	}
}
