package main

import (
	"encoding/json"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"tablepro-fyne/internal/db"
)

// uiSettings is the persisted window/layout state: what a user expects a
// desktop client to remember between runs. It lives next to the executable
// like connections.json, and is written only on close (single UI-thread
// writer), so no locking is needed here.
type uiSettings struct {
	Theme        string          `json:"theme"` // "" = follow system, "light", "dark"
	WindowWidth  float32         `json:"windowWidth"`
	WindowHeight float32         `json:"windowHeight"`
	SidebarRatio float32         `json:"sidebarRatio"`
	EditorRatio  float32         `json:"editorRatio"`
	ActiveTab    int             `json:"activeTab"`
	PageSize     int             `json:"pageSize,omitempty"`
	Tabs         []queryTabDraft `json:"tabs,omitempty"`
}

// queryTabDraft is one editor tab's unsaved work: the SQL text plus whether
// it was edited since its last run, so the ● marker survives a restart.
type queryTabDraft struct {
	Content string `json:"content"`
	Dirty   bool   `json:"dirty,omitempty"`
}

func uiSettingsPath() string {
	return db.StateFilePath("ui.json")
}

func loadUISettings(path string) uiSettings {
	s := uiSettings{}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s)
	return s
}

func saveUISettings(path string, s uiSettings) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

// applyTheme maps the stored choice onto the app settings; an empty value
// keeps fyne's system-following default.
func applyTheme(themeChoice string) {
	a := fyne.CurrentApp()
	switch themeChoice {
	case "light":
		a.Settings().SetTheme(theme.LightTheme())
	case "dark":
		a.Settings().SetTheme(theme.DarkTheme())
	default:
		a.Settings().SetTheme(theme.DefaultTheme())
	}
}
