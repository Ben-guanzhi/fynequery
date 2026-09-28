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

	c.histFilter = ""
	c.rebuildHistoryView()
	if len(c.historyView) != 3 || c.historyView[0].ID != 3 {
		t.Fatalf("unfiltered view = %+v, want 3 entries newest first", c.historyView)
	}

	c.histFilter = "users"
	c.rebuildHistoryView()
	if len(c.historyView) != 2 || c.historyView[0].ID != 3 || c.historyView[1].ID != 1 {
		t.Fatalf("filtered view = %+v, want ids [3 1]", c.historyView)
	}

	// The Table field is searched too.
	c.histFilter = "orders"
	c.rebuildHistoryView()
	if len(c.historyView) != 1 || c.historyView[0].ID != 2 {
		t.Fatalf("table-filtered view = %+v, want id 2", c.historyView)
	}

	// The database field is searched as well.
	c.histFilter = "zs"
	entries := c.store.History()
	entries[1].Database = "zs"
	c.store.ReplaceHistory(entries)
	c.rebuildHistoryView()
	if len(c.historyView) != 1 || c.historyView[0].ID != 2 {
		t.Fatalf("db-filtered view = %+v, want id 2", c.historyView)
	}

	c.histFilter = "no such thing"
	c.rebuildHistoryView()
	if len(c.historyView) != 0 {
		t.Fatalf("no-match view = %+v, want empty", c.historyView)
	}
}
