package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
)

// The favorites panel is the half of TablePlus's history panel that was
// missing, so what these pin is the click path: one keystroke saves what the
// editor shows, the same statement twice is not two entries, and loading one
// puts it back exactly as it was saved.

func newFavUIClient(t *testing.T) *Client {
	t.Helper()
	// The suite shares one state directory (TestMain), which would let one
	// test's favorites show up in the next one's panel. Favorites persist on
	// purpose, so the isolation has to be the directory.
	t.Setenv("TABLEPRO_STATE_DIR", t.TempDir())
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	// The whole UI, not just the client: the panel under test is the tab strip
	// buildUI assembles, and addQueryTab needs it too.
	w.SetContent(c.buildUI())
	return c
}

// TestFavoriteCurrentStatementSavesTheEditor is Ctrl/Cmd+B: the statement
// worth keeping is usually the one on screen, and it reaches disk without
// another dialog - see TestSaveFavoriteReachesDiskAndComesBack.
func TestFavoriteCurrentStatementSavesTheEditor(t *testing.T) {
	c := newFavUIClient(t)
	c.tabs.entry.SetText("SELECT * FROM orders WHERE status = 1")

	c.favoriteCurrentStatement()
	got := c.store.Favorites()
	if len(got) != 1 || got[0].SQL != "SELECT * FROM orders WHERE status = 1" {
		t.Fatalf("favorites = %+v, want the editor's statement", got)
	}
	if len(c.fav.view) != 1 {
		t.Errorf("panel view = %d rows, want 1 (saving must show it)", len(c.fav.view))
	}
}

// TestFavoriteCurrentStatementRefusesADuplicate is the case that would
// otherwise fill the list: saving twice is one entry, and the second press
// says so instead of pretending.
func TestFavoriteCurrentStatementRefusesADuplicate(t *testing.T) {
	c := newFavUIClient(t)
	c.tabs.entry.SetText("SELECT * FROM orders")
	c.favoriteCurrentStatement()

	c.tabs.entry.SetText("SELECT   *\nFROM orders") // reformatted, same statement
	c.favoriteCurrentStatement()
	if got := c.store.Favorites(); len(got) != 1 {
		t.Fatalf("favorites = %+v, want the one entry", got)
	}
	if !strings.Contains(c.statusText(), "已经收藏过") {
		t.Errorf("status = %q, want the duplicate to be reported", c.statusText())
	}
}

// TestFavoriteCurrentStatementRefusesAnEmptyEditor keeps an empty tab from
// producing a row with nothing to load back.
func TestFavoriteCurrentStatementRefusesAnEmptyEditor(t *testing.T) {
	c := newFavUIClient(t)
	c.tabs.entry.SetText("   ")
	c.favoriteCurrentStatement()
	if len(c.store.Favorites()) != 0 {
		t.Error("an empty editor must not be favourited")
	}
	if !strings.Contains(c.statusText(), "没有可收藏") {
		t.Errorf("status = %q, want it to say there is nothing to save", c.statusText())
	}
}

// TestLoadFavoritePutsTheStatementBack is what the list is for: a saved
// statement comes back with its line breaks, not squeezed onto one line the
// way the row shows it.
func TestLoadFavoritePutsTheStatementBack(t *testing.T) {
	c := newFavUIClient(t)
	sql := "SELECT id,\n       name\nFROM orders"
	c.tabs.entry.SetText(sql)
	c.favoriteCurrentStatement()

	c.tabs.entry.SetText("something else")
	c.loadFavorite(c.store.Favorites()[0])
	if c.tabs.entry.Text != sql {
		t.Errorf("editor = %q, want the saved statement back as it was", c.tabs.entry.Text)
	}
}

// TestDeleteSelectedFavorite drops the one row the panel has selected.
func TestDeleteSelectedFavorite(t *testing.T) {
	c := newFavUIClient(t)
	c.tabs.entry.SetText("SELECT 1")
	c.favoriteCurrentStatement()
	c.tabs.entry.SetText("SELECT 2")
	c.favoriteCurrentStatement()

	c.fav.selected = 0 // newest first, so this is SELECT 2
	c.deleteSelectedFavorite()
	got := c.store.Favorites()
	if len(got) != 1 || got[0].SQL != "SELECT 1" {
		t.Fatalf("favorites = %+v, want only SELECT 1", got)
	}
}

// TestDeleteFavoriteWithoutASelection says what is missing instead of
// deleting the wrong row: the panel's selection is dropped with every
// re-filter, so "none selected" is a real state.
func TestDeleteFavoriteWithoutASelection(t *testing.T) {
	c := newFavUIClient(t)
	c.tabs.entry.SetText("SELECT 1")
	c.favoriteCurrentStatement()

	c.fav.selected = -1
	c.deleteSelectedFavorite()
	if len(c.store.Favorites()) != 1 {
		t.Error("no selection must delete nothing")
	}
	if !strings.Contains(c.statusText(), "先选中") {
		t.Errorf("status = %q, want it to ask for a selection", c.statusText())
	}
}

// TestFavoritePanelShowsNewestFirst is the panel's side of the ordering: the
// store hands the list the file's order and the list reverses it, so what you
// just saved is on top - the same thing the 历史 tab does with the log.
func TestFavoritePanelShowsNewestFirst(t *testing.T) {
	c := newFavUIClient(t)
	c.tabs.entry.SetText("SELECT 1")
	c.favoriteCurrentStatement()
	c.tabs.entry.SetText("SELECT 2")
	c.favoriteCurrentStatement()

	if len(c.fav.view) != 2 || c.fav.view[0].SQL != "SELECT 2" {
		t.Fatalf("panel view = %+v, want SELECT 2 on top", c.fav.view)
	}
}

// TestFavoriteFiltersByName keeps the two tabs' filter boxes working the same
// way: a saved statement is found by its text, its label and its database.
func TestFavoriteFiltersByName(t *testing.T) {
	c := newFavUIClient(t)
	c.tabs.entry.SetText("SELECT * FROM orders")
	c.store.SaveFavorite("c0", "shop", "SELECT * FROM orders")
	c.tabs.entry.SetText("SELECT * FROM invoices")
	c.store.SaveFavorite("c0", "erp", "SELECT * FROM invoices")
	c.refreshFavorites()

	c.fav.filter = "invoice"
	c.fav.refilter()
	if len(c.fav.view) != 1 {
		t.Fatalf("filtered view = %d rows, want 1", len(c.fav.view))
	}
	c.fav.filter = "erp"
	c.fav.refilter()
	if len(c.fav.view) != 1 {
		t.Fatalf("database-filtered view = %d rows, want 1", len(c.fav.view))
	}
	c.fav.filter = "no such thing"
	c.fav.refilter()
	if len(c.fav.view) != 0 {
		t.Fatalf("no-match view = %d rows, want 0", len(c.fav.view))
	}
}

// TestFavoriteShortcutIsDocumented pins the other half of adding a combo: a
// binding registered through bindKey shows up in the 快捷键 dialog, because
// the description is registered where the binding is.
func TestFavoriteShortcutIsDocumented(t *testing.T) {
	c, _ := newKeyClient(t)
	for _, line := range c.keyDocs {
		if strings.Contains(line, "收藏当前标签里的语句") {
			return
		}
	}
	t.Error("Ctrl/Cmd+B is bound but missing from the 快捷键 dialog")
}

// statusText is what the status bar currently says; it is nil-safe because a
// test client that never built the grid has no status label.
func (c *Client) statusText() string {
	if c.status == nil {
		return ""
	}
	return c.status.Text
}
