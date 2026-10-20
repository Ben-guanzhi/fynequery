package main

// Exports already reach four formats - but only through a file dialog. These
// tests pin 「复制为」, which puts the same renderers on the clipboard.

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// newGridClient builds a client holding a two-column, two-row result. The grid
// is the only input the copy path reads, so no connection is needed.
func newGridClient(t *testing.T) (*Client, fyne.App) {
	t.Helper()
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildUI())
	c.grid.header = []string{"id (int)", "name (text)"}
	c.grid.cols = []string{"id", "name"}
	c.grid.colTypes = []string{"int", "text"}
	c.grid.rows = [][]string{{"1", "alice"}, {"2", "bo, b"}}
	c.grid.lastRows = [][]interface{}{{int64(1), "alice"}, {int64(2), "bo, b"}}
	return c, a
}

func TestCopyResultAsCSVQuotesWhatNeedsIt(t *testing.T) {
	c, a := newGridClient(t)
	c.copyResultAs("csv")
	// The header keeps the plain column name, and a value with a comma is
	// quoted - otherwise it would land in two cells in the target program.
	if got, want := a.Clipboard().Content(), "id,name\n1,alice\n2,\"bo, b\"\n"; got != want {
		t.Fatalf("clipboard = %q, want %q", got, want)
	}
	if !strings.Contains(c.status.Text, "已复制 2 行") {
		t.Errorf("status = %q, want the copied row count", c.status.Text)
	}
}

func TestCopyResultAsJSON(t *testing.T) {
	c, a := newGridClient(t)
	c.copyResultAs("json")
	got := a.Clipboard().Content()
	for _, want := range []string{`"id": "1"`, `"name": "alice"`, `"name": "bo, b"`} {
		if !strings.Contains(got, want) {
			t.Errorf("clipboard = %s, want it to contain %s", got, want)
		}
	}
}

// TestCopyInsertRefusesAReadOnlyResult: without a single-table source there is
// no table name to write, and guessing one is how an INSERT lands somewhere
// nobody picked.
func TestCopyInsertRefusesAReadOnlyResult(t *testing.T) {
	c, a := newGridClient(t)
	a.Clipboard().SetContent("untouched")
	c.grid.src = db.TableRef{} // zero value: no source table

	c.copyResultAs("insert")

	if got := a.Clipboard().Content(); got != "untouched" {
		t.Errorf("clipboard = %q, want it left alone", got)
	}
	if !strings.Contains(c.status.Text, "INSERT") {
		t.Errorf("status = %q, want the reason nothing was copied", c.status.Text)
	}
}

// TestCopyInsertUsesTheCatalogTypes runs the real round trip against SQLite:
// the INSERT form needs each column's declared type, which only the catalog
// knows, so the copy goes through background and lands on the clipboard when it
// comes back.
func TestCopyInsertUsesTheCatalogTypes(t *testing.T) {
	c := newPagingClient(t, 3, false, "100")
	c.tabs.entry.setTextSilently("SELECT * FROM items ORDER BY id")
	c.runCurrent()

	c.copyResultAs("insert")

	got := fyne.CurrentApp().Clipboard().Content()
	if !strings.Contains(got, "INSERT INTO") {
		t.Fatalf("clipboard = %q, want INSERT statements", got)
	}
	if !strings.Contains(got, "'row-1'") {
		t.Errorf("clipboard = %q, want the rows that were on screen", got)
	}
}
