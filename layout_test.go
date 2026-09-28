package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// TestSidebarTreeFillsHeight renders the real main window on the test canvas
// and asserts the navigator tree absorbs the sidebar's leftover height. This
// is the regression test for the "树区太小" report: a VBox wrapper used to
// give the tree only its minimum height.
func TestSidebarTreeFillsHeight(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := &Client{
		app:        a,
		win:        w,
		store:      db.NewStore(),
		treeNodes:  make(map[string]*nodeData),
		expanded:   make(map[string]bool),
		sugTables:  make(map[string][]string),
		sugLoading: make(map[string]bool),
		fkCache:    make(map[string][]db.ForeignKeyInfo),
		edits:      newGridEditState(),
		sortCol:    -1,
	}
	content := c.buildUI()
	w.SetContent(content)
	w.Resize(fyne.NewSize(1200, 900))

	if c.schemaRows == nil {
		t.Fatal("schema rows not built")
	}
	h := c.schemaRows.Size().Height
	// One simulated database row keeps the tree non-empty.
	if h < 200 {
		t.Fatalf("tree viewport height = %.0f, want a tall fill (>=200)", h)
	}
}

// TestEditorReceivesTypedText drives the real canvas focus pipeline: focus the
// editor and type through the canvas, the way the driver does after a click.
func TestEditorReceivesTypedText(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := &Client{
		app:        a,
		win:        w,
		store:      db.NewStore(),
		treeNodes:  make(map[string]*nodeData),
		expanded:   make(map[string]bool),
		sugTables:  make(map[string][]string),
		sugLoading: make(map[string]bool),
		fkCache:    make(map[string][]db.ForeignKeyInfo),
		edits:      newGridEditState(),
		sortCol:    -1,
	}
	w.SetContent(c.buildUI())

	if c.queryEntry == nil {
		t.Fatal("query entry not built")
	}
	// Mimic the real glfw driver: char input goes to canvas.Focused().
	w.Canvas().SetOnTypedRune(func(r rune) {
		if f := w.Canvas().Focused(); f != nil {
			f.TypedRune(r)
		}
	})
	w.Canvas().Focus(c.queryEntry)
	if w.Canvas().Focused() == nil {
		t.Fatal("editor did not take focus")
	}
	test.TypeOnCanvas(w.Canvas(), "SELECT")
	if got := c.queryEntry.Text; got != "SELECT" {
		t.Fatalf("typed text = %q, want SELECT", got)
	}
}
