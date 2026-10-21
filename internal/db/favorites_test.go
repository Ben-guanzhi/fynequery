package db

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Favorites are this client's own file, so unlike history there is nothing to
// merge and nothing to throttle: the tests below are mostly about what a
// deliberate click is supposed to do twice.

func newFavStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TABLEPRO_STATE_DIR", dir)
	s := &Store{live: make(map[string]*Conn), favoritesFile: filepath.Join(dir, "favorites.json")}
	return s, s.favoritesFile
}

func readFavoritesFile(t *testing.T, file string) []FavoriteEntry {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read: %v", err)
	}
	var entries []FavoriteEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return entries
}

// TestSaveFavoriteReachesDiskAndComesBack is the whole point: a favourite is
// kept because it will be run again, usually in a later session. Unlike
// history there is no flush to wait for - the click is the write.
func TestSaveFavoriteReachesDiskAndComesBack(t *testing.T) {
	s, file := newFavStore(t)

	entry, added := s.SaveFavorite("c0", "shop", "SELECT * FROM orders")
	if !added {
		t.Fatal("the first save must report it added the entry")
	}
	if entry.ID == 0 {
		t.Error("the entry needs an id the list can delete it by")
	}
	if got := readFavoritesFile(t, file); len(got) != 1 || got[0].SQL != "SELECT * FROM orders" {
		t.Fatalf("on-disk entries = %+v, want the one statement", got)
	}

	// A second store is a second launch: what it reads is what the user gets.
	again := &Store{live: make(map[string]*Conn), favoritesFile: file}
	again.loadFavorites()
	got := again.Favorites()
	if len(got) != 1 || got[0].SQL != "SELECT * FROM orders" || got[0].Database != "shop" {
		t.Fatalf("after a relaunch = %+v, want the same entry", got)
	}
}

// TestFavoritesKeepInsertionOrder pins the shape the file is written in, which
// is also the shape History() answers with. The panel shows them the other way
// round and says so in its own test - reversing here would leave a second
// caller guessing which order it got.
func TestFavoritesKeepInsertionOrder(t *testing.T) {
	s, _ := newFavStore(t)
	s.SaveFavorite("c0", "db", "SELECT 1")
	s.SaveFavorite("c0", "db", "SELECT 2")

	got := s.Favorites()
	if len(got) != 2 || got[0].SQL != "SELECT 1" || got[1].SQL != "SELECT 2" {
		t.Fatalf("Favorites() = %+v, want the order they were saved in", got)
	}
}

// TestSaveFavoriteRefusesADuplicate is the case a throttle would have hidden:
// favouriting the same statement twice - or again after a Ctrl+Shift+F - must
// not pile up near-duplicates, and it must not move the entry either.
func TestSaveFavoriteRefusesADuplicate(t *testing.T) {
	s, file := newFavStore(t)
	s.SaveFavorite("c0", "db", "SELECT * FROM orders")
	s.SaveFavorite("c0", "db", "SELECT 1")

	entry, added := s.SaveFavorite("c0", "db", "SELECT   *\nFROM orders")
	if added {
		t.Error("the same statement under different spacing must not be saved twice")
	}
	if entry.SQL != "SELECT * FROM orders" {
		t.Errorf("returned entry = %+v, want the one already there", entry)
	}
	if got := s.Favorites(); len(got) != 2 || got[0].SQL != "SELECT * FROM orders" {
		t.Errorf("list = %+v, want 2 entries with the original still first", got)
	}
	if got := readFavoritesFile(t, file); len(got) != 2 {
		t.Errorf("on-disk entries = %d, want 2 (a refused save rewrites nothing new)", len(got))
	}
}

func TestSaveFavoriteRefusesAnEmptyStatement(t *testing.T) {
	s, _ := newFavStore(t)
	if _, added := s.SaveFavorite("c0", "db", "   \n  "); added {
		t.Error("an empty statement has nothing to load back and must be refused")
	}
	if len(s.Favorites()) != 0 {
		t.Error("a refused save must leave the list alone")
	}
}

// TestDeleteFavorite removes one entry and persists it, so a deletion survives
// the session it happened in.
func TestDeleteFavorite(t *testing.T) {
	s, file := newFavStore(t)
	first, _ := s.SaveFavorite("c0", "db", "SELECT 1")
	s.SaveFavorite("c0", "db", "SELECT 2")

	if !s.DeleteFavorite(first.ID) {
		t.Fatal("deleting an id that is in the list must report it found")
	}
	got := s.Favorites()
	if len(got) != 1 || got[0].SQL != "SELECT 2" {
		t.Fatalf("list = %+v, want only SELECT 2", got)
	}
	if onDisk := readFavoritesFile(t, file); len(onDisk) != 1 || onDisk[0].SQL != "SELECT 2" {
		t.Errorf("on-disk entries = %+v, want the deletion persisted", onDisk)
	}
	if s.DeleteFavorite(first.ID) {
		t.Error("deleting the same id twice must report it not found")
	}
}

func TestClearFavorites(t *testing.T) {
	s, file := newFavStore(t)
	s.SaveFavorite("c0", "db", "SELECT 1")
	if n := s.ClearFavorites(); n != 1 {
		t.Errorf("ClearFavorites() = %d, want the one it dropped", n)
	}
	if got := readFavoritesFile(t, file); len(got) != 0 {
		t.Errorf("on-disk entries = %+v, want none", got)
	}
}

// TestFavoritesFileThatDoesNotParseIsMovedAside is the corrupt-file path the
// shared reader gives every state file: the bytes stay recoverable under
// .corrupt instead of being overwritten by the next save.
func TestFavoritesFileThatDoesNotParseIsMovedAside(t *testing.T) {
	s, file := newFavStore(t)
	if err := os.WriteFile(file, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.loadFavorites()
	if len(s.Favorites()) != 0 {
		t.Error("a file that does not parse must load as empty, not half of it")
	}
	if _, err := os.Stat(file + ".corrupt"); err != nil {
		t.Errorf("the bad file was not moved aside: %v", err)
	}

	// And the next save still works, which is the part a panic would lose.
	if _, added := s.SaveFavorite("c0", "db", "SELECT 1"); !added {
		t.Fatal("saving after a corrupt file must work")
	}
	if got := readFavoritesFile(t, file); len(got) != 1 {
		t.Errorf("on-disk entries = %+v, want the new one", got)
	}
}

// TestFavoriteMatchesFilter is what the panel's filter box asks of an entry:
// it must answer across the statement, its label and its database.
func TestFavoriteMatchesFilter(t *testing.T) {
	e := FavoriteEntry{SQL: "SELECT * FROM Orders", Name: "daily", Database: "shop"}
	e.fillLowerCache()
	if !e.MatchesFilter("orders") || !e.MatchesFilter("daily") || !e.MatchesFilter("shop") {
		t.Error("the filter must match the statement, the name and the database")
	}
	if e.MatchesFilter("invoices") {
		t.Error("a filter that is not there must not match")
	}
}
