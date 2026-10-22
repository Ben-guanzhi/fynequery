package main

import "testing"

// writeConns persists conns to the (test-isolated) config dir.
func writeConns(t *testing.T, conns ...Connection) {
	t.Helper()
	if err := saveConnections(conns); err != nil {
		t.Fatalf("seed connections: %v", err)
	}
}

// readConns loads the saved connections from the (test-isolated) config dir.
func readConns(t *testing.T) []Connection {
	t.Helper()
	conns, err := loadConnections()
	if err != nil {
		t.Fatalf("load connections: %v", err)
	}
	return conns
}

// TestConfigDirOverride checks the FYNEQUERY_CONFIG_DIR seam the edit tests
// rely on for isolation (unset → the real user config dir).
func TestConfigDirOverride(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", "/tmp/fq-override")
	if got := configDir(); got != "/tmp/fq-override" {
		t.Fatalf("configDir override: got %q", got)
	}
}

// TestApplyConnectionEditUpdatesInPlace verifies a same-name edit rewrites the
// entry and keeps Group/Tags from the original (the sheet never edits them),
// even when the caller passes bogus values.
func TestApplyConnectionEditUpdatesInPlace(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	orig := Connection{Name: "prod", Driver: "mysql", Host: "old", Port: "3306",
		Group: "Prod", Tags: []string{"live"}, UseKeyring: false}
	writeConns(t, orig)

	edited := orig
	edited.Host = "new-host"
	edited.Port = "3307"
	edited.Group = "SHOULD_BE_IGNORED" // must be overwritten from orig
	edited.Tags = []string{"nope"}     // ditto

	if err := applyConnectionEdit(orig, edited); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := readConns(t)
	if len(got) != 1 {
		t.Fatalf("want 1 connection, got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Host != "new-host" || c.Port != "3307" {
		t.Errorf("edited fields not applied: %+v", c)
	}
	if c.Group != "Prod" || len(c.Tags) != 1 || c.Tags[0] != "live" {
		t.Errorf("group/tags not preserved from original: %+v", c)
	}
}

// TestApplyConnectionEditRename checks that renaming removes the old entry and
// inserts exactly one under the new name, preserving group/tags.
func TestApplyConnectionEditRename(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	orig := Connection{Name: "old", Driver: "postgres", Host: "h",
		Group: "Work", Tags: []string{"loc"}, UseKeyring: false}
	writeConns(t, orig, Connection{Name: "other", Driver: "mysql", UseKeyring: false})

	edited := orig
	edited.Name = "new"
	edited.Host = "h2"

	if err := applyConnectionEdit(orig, edited); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := readConns(t)
	if len(got) != 2 {
		t.Fatalf("want 2 connections after rename, got %d: %+v", len(got), got)
	}
	var renamed, other *Connection
	for i := range got {
		switch got[i].Name {
		case "new":
			renamed = &got[i]
		case "other":
			other = &got[i]
		default:
			t.Errorf("stale entry %q survived rename", got[i].Name)
		}
	}
	if renamed == nil || renamed.Host != "h2" {
		t.Fatalf("renamed entry wrong: %+v", renamed)
	}
	if renamed.Group != "Work" || len(renamed.Tags) != 1 {
		t.Errorf("group/tags not preserved on rename: %+v", renamed)
	}
	if other == nil {
		t.Error("unrelated connection was disturbed")
	}
}

// TestApplyConnectionEditEmptyName guards that an empty name is rejected before
// any store mutation, leaving the saved connections untouched.
func TestApplyConnectionEditEmptyName(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	orig := Connection{Name: "keep", Driver: "mysql", Host: "h", UseKeyring: false}
	writeConns(t, orig)

	edited := orig
	edited.Name = ""
	if err := applyConnectionEdit(orig, edited); err == nil {
		t.Fatal("expected error for empty connection name")
	}
	got := readConns(t)
	if len(got) != 1 || got[0].Name != "keep" {
		t.Errorf("store must be unchanged on empty-name error: %+v", got)
	}
}
