package main

import (
	"runtime"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// TestSecretStorageWarning pins the dialog's honesty about saved passwords:
// the data layer answers whether passwords are actually sealed — always on
// Windows, probed from the system keyring elsewhere — and the note names the
// clear-text risk exactly when they are not, never claiming encryption that
// did not happen.
func TestSecretStorageWarning(t *testing.T) {
	// Windows seals with DPAPI unconditionally; a probe could never say false.
	if runtime.GOOS == "windows" && !db.SecretsSealed() {
		t.Fatal("Windows must report sealed passwords")
	}
	if note := secretStorageNote(true); note != "" {
		t.Errorf("sealed platforms should stay quiet, got %q", note)
	}
	note := secretStorageNote(false)
	if !strings.Contains(note, "明文") {
		t.Errorf("unsealed warning must name the clear-text storage, got %q", note)
	}
	if !strings.Contains(note, "0600") {
		t.Errorf("unsealed warning must say what does protect the file, got %q", note)
	}
}

// TestSSLModesByEngine pins the 加密 dropdown's label-to-mode mapping: SQL
// Server gets the modes its driver actually parses (with verify-and-encrypt as
// the default entry), MySQL only the one value its connector acts on, and a
// stored mode the current engine does not name survives the switch instead of
// being silently dropped.
func TestSSLModesByEngine(t *testing.T) {
	mssql := sslChoicesFor(db.DbTypeSQLServer)
	for label, want := range map[string]string{
		"加密并校验证书（默认）":    "true",
		"加密，信任服务器证书（自签）": "trust",
		"不加密": "disable",
	} {
		if got := sslValueFor(mssql, label); got != want {
			t.Errorf("mssql label %q = %q, want %q", label, got, want)
		}
	}
	// The default entry must be the first one the dialog offers.
	if sslOptionLabels(mssql, "")[0] != "加密并校验证书（默认）" {
		t.Errorf("mssql options = %v, want verify-and-encrypt first", sslOptionLabels(mssql, ""))
	}

	// "strict" is not offered: this driver cannot parse it, and a mode that
	// silently degrades is the bug this list exists to prevent.
	for _, ch := range sslChoicesFor(db.DbTypeSQLServer) {
		if ch.value == "strict" {
			t.Error("SQL Server must not offer a mode its driver rejects")
		}
	}

	if got := sslValueFor(sslChoicesFor(db.DbTypeMySQL), "加密"); got != "true" {
		t.Errorf("mysql 加密 = %q, want true (the only value the connector acts on)", got)
	}

	// A mode from another engine's vocabulary is carried through as its own
	// label and maps back to itself.
	carry := "verify-full"
	found := false
	for _, label := range sslOptionLabels(mssql, carry) {
		if label == carry {
			found = true
		}
	}
	if !found {
		t.Errorf("labels for mssql with carried mode = %v, want %q kept", sslOptionLabels(mssql, carry), carry)
	}
	if got := sslValueFor(mssql, sslChoiceLabel(mssql, carry)); got != carry {
		t.Errorf("round trip = %q, want %q", got, carry)
	}

	// SQLite and Oracle have no TLS list of their own; they keep the list the
	// dialog already stored modes come from, rather than losing them.
	if len(sslChoicesFor(db.DbTypeSQLite)) != len(sslChoicesFor(db.DbTypePostgres)) {
		t.Error("engines without a TLS vocabulary must fall back, not go blank")
	}
}

// TestPatternListRoundTrip pins the filter box format: text the user writes has
// to read back as the list that gets stored, so reopening a connection shows
// what is really in effect. Both separators people type are accepted, and
// blanks and repeats do not pile up.
func TestPatternListRoundTrip(t *testing.T) {
	cases := []struct {
		text, want string
	}{
		{"", ""},
		{"  \n \t ", ""},
		{"zs", "zs"},
		{"zs\nUFT*", "zs\nUFT*"},
		{"zs, UFT*", "zs\nUFT*"},
		{"zs\nzs\n", "zs"},
		{"  APP* , APP* , *_env  ", "APP*\n*_env"},
	}
	for _, tc := range cases {
		if got := renderPatterns(parsePatterns(tc.text)); got != tc.want {
			t.Errorf("parse+render(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// TestFiltersDiffer drives the one guard that decides whether saving reloads the
// navigator: the same list written in a different way is not a change, a
// pattern added is.
func TestFiltersDiffer(t *testing.T) {
	a := db.DatabaseFilter{Include: []string{"zs", "u_*"}}
	b := db.DatabaseFilter{Include: []string{"zs", "u_*"}}
	if filtersDiffer(a, b) {
		t.Error("identical filters reported a change")
	}
	if !filtersDiffer(a, db.DatabaseFilter{Include: []string{"zs"}}) {
		t.Error("a dropped pattern is a change")
	}
	if !filtersDiffer(db.DatabaseFilter{}, a) {
		t.Error("going from nothing to something is a change")
	}
}

// TestFilterFieldsRoundTripThroughConfig covers the box → config → box trip the
// edit dialog relies on.
func TestFilterFieldsRoundTripThroughConfig(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)

	edit := db.ConnConfig{ID: "c0", Name: "prod", Type: db.DbTypePostgres, Host: "h", Port: 5432,
		DatabaseFilter: db.DatabaseFilter{Include: []string{"zs", "*_env"}, Exclude: []string{"tempdb"}}}
	f := c.newConnFields(&edit)
	if got := f.includeE.Text; got != "zs\n*_env" {
		t.Fatalf("seeded include box = %q, want one pattern per line", got)
	}
	cfg := f.config()
	if got := cfg.DatabaseFilter.Include; len(got) != 2 || got[0] != "zs" || got[1] != "*_env" {
		t.Errorf("include = %q, want [zs *_env]", got)
	}
	if got := cfg.DatabaseFilter.Exclude; len(got) != 1 || got[0] != "tempdb" {
		t.Errorf("exclude = %q, want [tempdb]", got)
	}

	// A new connection hides nothing.
	if fresh := c.newConnFields(nil).config(); len(fresh.DatabaseFilter.Include)+len(fresh.DatabaseFilter.Exclude) != 0 {
		t.Errorf("new connection starts with a filter: %+v", fresh.DatabaseFilter)
	}

	// Both boxes have to be reachable from the form, or they are widgets nothing
	// shows.
	laid := test.LaidOutObjects(f.form())
	for _, want := range []fyne.CanvasObject{f.includeE, f.excludeE} {
		found := false
		for _, o := range laid {
			if o == want {
				found = true
			}
		}
		if !found {
			t.Error("a filter box is missing from the form")
		}
	}
}

// TestSQLiteDisablesTheFilterBoxes: nothing can be hidden from SQLite's single
// database, so the boxes say so instead of accepting text that does nothing.
// Switching the engine back has to re-enable them.
func TestSQLiteDisablesTheFilterBoxes(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)

	f := c.newConnFields(&db.ConnConfig{ID: "c0", Type: db.DbTypeSQLite, Database: "app.db"})
	if !f.includeE.Disabled() || !f.excludeE.Disabled() {
		t.Error("SQLite must disable both filter boxes")
	}
	// SetSelected runs the same handler the dropdown does.
	f.typeSel.SetSelected("PostgreSQL")
	if f.includeE.Disabled() || f.excludeE.Disabled() {
		t.Error("switching to a server engine must give the filter back")
	}
	if !strings.Contains(filterNote, "SQLite") || !strings.Contains(filterNote, "*") {
		t.Errorf("the note must name the masks and the SQLite exception: %q", filterNote)
	}
}

// TestConnDialogShowsAll: a custom dialog keeps the size it was handed, so
// content taller than that sits below the bottom edge unseen - which is exactly
// how a form row added later would silently stop existing. Both note stacks get
// measured: the clear-text warning only appears on platforms that need it, and
// the taller stack is the one that decides the size.
func TestConnDialogShowsAll(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	f := c.newConnFields(&db.ConnConfig{ID: "c0", Type: db.DbTypePostgres, Host: "h", Port: 5432,
		DatabaseFilter: db.DatabaseFilter{Include: []string{"APP*", "*_env", "zs"}, Exclude: []string{"tempdb"}}})
	buttons := container.NewHBox(widget.NewButton("测试", func() {}), widget.NewButton("保存", func() {}))
	for _, sealed := range []bool{true, false} {
		min := connDialogContent(f, connNotes(sealed), buttons).MinSize()
		if min.Height > connDialogSize.Height || min.Width > connDialogSize.Width {
			t.Fatalf("sealed=%v: dialog opens at %v but its content needs %v", sealed, connDialogSize, min)
		}
	}
}
