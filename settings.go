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
	Theme        string  `json:"theme"` // "" = follow system, "light", "dark"
	WindowWidth  float32 `json:"windowWidth"`
	WindowHeight float32 `json:"windowHeight"`
	SidebarRatio float32 `json:"sidebarRatio"`
	EditorRatio  float32 `json:"editorRatio"`
	ActiveTab    int     `json:"activeTab"`
	PageSize     int     `json:"pageSize,omitempty"`
	// Toolbar choices that used to reset on every start: the 分页浏览 switch,
	// the 行数上限 selection ("" = default) and the details pane's open state.
	PagingOn  bool   `json:"pagingOn,omitempty"`
	RowLimit  string `json:"rowLimit,omitempty"`
	DetailsOn bool   `json:"detailsOn,omitempty"`
	// The session's last standing, pre-selected on start - the picker shows it
	// but the connection is not opened, 连接 still gets the tap.
	LastConnectionID string          `json:"lastConnectionID,omitempty"`
	LastDatabase     string          `json:"lastDatabase,omitempty"`
	Tabs             []queryTabDraft `json:"tabs,omitempty"`
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

// applyTheme maps the stored choice onto the app settings. Every choice gets
// the TablePlus palette; only the variant differs, and an empty choice leaves
// the variant to the system.
//
// It used to set fyne's LightTheme()/DarkTheme() directly. Those two ignore the
// user's preference - which is exactly why they are deprecated - and they carry
// the built-in palette; the variant is now a field on our own theme instead.
func applyTheme(themeChoice string) {
	a := fyne.CurrentApp()
	switch themeChoice {
	case "light":
		a.Settings().SetTheme(newTablePlusTheme(theme.VariantLight))
	case "dark":
		a.Settings().SetTheme(newTablePlusTheme(theme.VariantDark))
	default:
		a.Settings().SetTheme(newTablePlusTheme(tpFollow))
	}
}
