package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

func newTestClient(a fyne.App, w fyne.Window) *Client {
	return &Client{
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
		// Test mode: no background goroutines, run inline for determinism.
		asyncRun: false,
		dispatch: func(f func()) { f() },
	}
}

// mimicTypedInput registers the canvas char callback the way the real driver
// does, so tests can drive typing through the full focus pipeline.
func mimicTypedInput(w fyne.Window) {
	w.Canvas().SetOnTypedRune(func(r rune) {
		if f := w.Canvas().Focused(); f != nil {
			f.TypedRune(r)
		}
	})
}

// TestQueryTabs checks the multi-tab editor: tabs are independent, switching
// re-points the active editor, closing falls back to a survivor, and the last
// tab cannot be closed.
func TestQueryTabs(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildQueryView())
	mimicTypedInput(w)

	if len(c.qItems) != 1 {
		t.Fatalf("initial tab count = %d, want 1", len(c.qItems))
	}
	first := c.queryEntry
	first.setTextSilently("SELECT 1")

	// A new tab starts empty and becomes the active editor.
	c.addQueryTab()
	if len(c.qItems) != 2 || c.qTabs.CurrentTabIndex() != 1 {
		t.Fatalf("tab count/index = %d/%d, want 2/1", len(c.qItems), c.qTabs.CurrentTabIndex())
	}
	if c.queryEntry == first || c.queryEntry.Text != "" {
		t.Fatalf("new tab editor is not a fresh active one")
	}

	// Typing lands in the active tab only.
	w.Canvas().Focus(c.queryEntry)
	test.TypeOnCanvas(w.Canvas(), "SELECT 2")
	if c.queryEntry.Text != "SELECT 2" {
		t.Fatalf("typed text = %q, want SELECT 2", c.queryEntry.Text)
	}
	if first.Text != "SELECT 1" {
		t.Fatalf("first tab text changed to %q", first.Text)
	}

	// Switching back re-points the alias and keeps both texts.
	c.qTabs.SelectIndex(0)
	if c.queryEntry != first {
		t.Fatal("switching back did not restore the first editor")
	}
	if got := c.queryEntry.Text; got != "SELECT 1" {
		t.Fatalf("first tab text = %q, want SELECT 1", got)
	}

	// Closing the active tab falls back to a survivor.
	c.closeActiveTab()
	if len(c.qItems) != 1 || c.queryEntry != c.qItems[0].entry {
		t.Fatalf("after close: %d tabs, wrong active editor", len(c.qItems))
	}

	// The last tab cannot be closed.
	c.closeActiveTab()
	if len(c.qItems) != 1 {
		t.Fatalf("last tab was closed, %d remain", len(c.qItems))
	}
}

// TestTabDirtyFlow drives the dirty marker end to end: edits (simulated
// keystrokes) mark the tab, running the query clears it, and copyCell puts
// the selected cell's value on the test clipboard.
func TestTabDirtyFlow(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "t.db")

	sdb, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sdb.Exec(`CREATE TABLE users (name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := sdb.Exec(`INSERT INTO users (name) VALUES ('alice')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sdb.Close()

	if err := c.connect(db.ConnConfig{ID: "c0", Name: "t", Type: db.DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })
	w.SetContent(c.buildUI())
	mimicTypedInput(w)

	if c.queryEntry == nil {
		t.Fatal("queryEntry nil after buildUI")
	}
	if c.queryEntry.OnChanged == nil {
		t.Fatal("OnChanged nil after buildUI")
	}
	c.queryEntry.setTextSilently("SELECT name FROM users")
	if c.queryEntry.OnChanged == nil {
		t.Fatal("OnChanged nil after setTextSilently")
	}
	c.queryEntry.OnChanged("") // what a real keystroke fires
	if !c.qItems[0].dirty {
		t.Fatal("editing did not mark the tab dirty")
	}
	if !strings.Contains(c.qItems[0].item.Text, "●") {
		t.Fatalf("dirty title = %q, want a ● marker", c.qItems[0].item.Text)
	}

	c.runCurrent()
	if c.qItems[0].dirty || strings.Contains(c.qItems[0].item.Text, "●") {
		t.Fatalf("running did not clear the dirty marker: %+v", c.qItems[0])
	}

	c.hasCell = true
	c.selectedCell = widget.TableCellID{Row: 0, Col: 0}
	c.copyCell()
	if got := a.Clipboard().Content(); got != "alice" {
		t.Fatalf("clipboard = %q, want alice", got)
	}
}

// TestCtrlEnterShortcut pins the fix: with the editor focused, glfw routes
// Ctrl/Cmd+Enter to the widget (an Entry is always Shortcutable), so
// suggestEntry.TypedShortcut must run the query instead of falling through
// to Entry as a newline. This is the exact delivery path the driver takes.
func TestCtrlEnterShortcut(t *testing.T) {
	test.NewApp()
	for _, tc := range []struct {
		name string
		mod  fyne.KeyModifier
		key  fyne.KeyName
	}{{"ctrl/return", fyne.KeyModifierControl, fyne.KeyReturn}, {"super/enter", fyne.KeyModifierSuper, fyne.KeyEnter}} {
		ran := false
		s := newSuggestEntry()
		s.setTextSilently("SELECT 1")
		s.onRun = func() { ran = true }
		s.TypedShortcut(&desktop.CustomShortcut{KeyName: tc.key, Modifier: tc.mod})
		if !ran {
			t.Fatalf("%s: Ctrl/Cmd+Enter did not run the query", tc.name)
		}
		if strings.Contains(s.Text, "\n") {
			t.Fatalf("%s: shortcut leaked through as a newline: %q", tc.name, s.Text)
		}
	}

	// an unrelated shortcut (Ctrl+Z undo) must still reach the embedded Entry
	ran := false
	s := newSuggestEntry()
	s.setTextSilently("SELECT 1")
	s.onRun = func() { ran = true }
	s.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyZ, Modifier: fyne.KeyModifierControl})
	if ran {
		t.Fatal("Ctrl+Z must not be mistaken for the run shortcut")
	}
}

// TestDraftSaveRestoreRoundTrip drives the whole loop: type into two tabs,
// close-save writes the drafts into ui.json, and a fresh client restoring
// from that file rebuilds the same tabs, texts and dirty markers.
func TestDraftSaveRestoreRoundTrip(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildQueryView())

	c.addQueryTab()
	c.qItems[0].entry.setTextSilently("SELECT 1")
	c.qItems[1].entry.setTextSilently("SELECT dirty")
	c.markTabDirty(c.qItems[1].entry)
	c.saveWindowSettings(w)

	loaded := loadUISettings(uiSettingsPath())
	if len(loaded.Tabs) != 2 || loaded.Tabs[0].Content != "SELECT 1" ||
		!loaded.Tabs[1].Dirty || loaded.Tabs[1].Content != "SELECT dirty" {
		t.Fatalf("drafts on disk = %+v", loaded.Tabs)
	}

	c2 := newTestClient(a, a.NewWindow("t2"))
	c2.uiSet = loaded
	c2.buildQueryView()
	if len(c2.qItems) != 2 {
		t.Fatalf("restored tabs = %d, want 2", len(c2.qItems))
	}
	if c2.qItems[0].entry.Text != "SELECT 1" || c2.qItems[0].dirty {
		t.Fatalf("restored tab 0 = %q dirty=%v", c2.qItems[0].entry.Text, c2.qItems[0].dirty)
	}
	if c2.qItems[1].entry.Text != "SELECT dirty" || !c2.qItems[1].dirty {
		t.Fatalf("restored tab 1 = %q dirty=%v", c2.qItems[1].entry.Text, c2.qItems[1].dirty)
	}
	if !strings.Contains(c2.qItems[1].item.Text, "●") {
		t.Fatalf("restored dirty title = %q, want a ● marker", c2.qItems[1].item.Text)
	}
}
