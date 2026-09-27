package main

import (
	"path/filepath"
	"testing"
)

// TestUISettingsRoundTrip verifies the persisted layout state survives a
// save/load cycle: theme choice, window geometry, split positions and the
// active query tab.
func TestUISettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.json")

	// A fresh install has defaults, not garbage.
	s := loadUISettings(path)
	if s.Theme != "" || s.ActiveTab != 0 || s.WindowWidth != 0 {
		t.Fatalf("default settings = %+v", s)
	}

	s = uiSettings{
		Theme: "dark", WindowWidth: 1440, WindowHeight: 900,
		SidebarRatio: 0.3, EditorRatio: 0.4, ActiveTab: 2,
	}
	saveUISettings(path, s)

	got := loadUISettings(path)
	if got != s {
		t.Fatalf("round trip = %+v, want %+v", got, s)
	}
}
