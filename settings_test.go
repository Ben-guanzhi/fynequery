package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestUISettingsRoundTrip verifies the persisted layout state survives a
// save/load cycle: theme choice, window geometry, split positions, the active
// query tab, and the editor tab drafts (SQL text + dirty markers).
func TestUISettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.json")

	// A fresh install has defaults, not garbage.
	s := loadUISettings(path)
	if s.Theme != "" || s.ActiveTab != 0 || s.WindowWidth != 0 {
		t.Fatalf("default settings = %+v", s)
	}

	s = uiSettings{
		Theme: "dark", WindowWidth: 1440, WindowHeight: 900,
		SidebarRatio: 0.3, EditorRatio: 0.4, ActiveTab: 2, PageSize: 50,
		Tabs: []queryTabDraft{
			{Content: "SELECT 1"},
			{Content: "SELECT 2", Dirty: true},
		},
	}
	saveUISettings(path, s)

	got := loadUISettings(path)
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("round trip = %+v, want %+v", got, s)
	}

	// Only the sizes the selector offers are accepted, so a stale or hand-edited
	// ui.json value falls back to the default at startup.
	if validPageSize(300) {
		t.Fatal("300 accepted as a page size")
	}
	if !validPageSize(defaultPageSize) {
		t.Fatalf("default %d rejected", defaultPageSize)
	}
}
