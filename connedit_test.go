package main

import (
	"os"
	"strings"
	"testing"

	"fynequery/db"
)

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

// TestUpsertConnectionCorruptFileKeepsData guards the data-loss regression: a
// corrupt connections.json must fail the upsert with an error and leave the
// file untouched — never overwrite it with a list holding only the new entry.
func TestUpsertConnectionCorruptFileKeepsData(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FYNEQUERY_CONFIG_DIR", dir)
	corrupt := []byte(`[{"name":"prod","driver":`) // truncated JSON
	if err := os.WriteFile(connectionsPath(), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	err := upsertConnection(Connection{Name: "new", Driver: "mysql", UseKeyring: false})
	if err == nil {
		t.Fatal("expected error when the saved file is corrupt")
	}
	got, readErr := os.ReadFile(connectionsPath())
	if readErr != nil {
		t.Fatalf("corrupt file was removed: %v", readErr)
	}
	if string(got) != string(corrupt) {
		t.Fatalf("corrupt file was overwritten: %q", got)
	}
}

// TestSaveConnectionsKeepsBackup verifies each save leaves the previous
// generation in connections.json.bak so a bad write is recoverable.
func TestSaveConnectionsKeepsBackup(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	writeConns(t, Connection{Name: "v1", Driver: "mysql", UseKeyring: false})

	if err := saveConnections([]Connection{{Name: "v2", Driver: "mysql", UseKeyring: false}}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	raw, err := os.ReadFile(connectionsPath() + ".bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	if !strings.Contains(string(raw), `"v1"`) || strings.Contains(string(raw), `"v2"`) {
		t.Fatalf("backup does not hold the previous generation: %s", raw)
	}
}

// The backup is for recovering the connection LIST, not secrets: a plaintext
// password in the previous generation must not survive into .bak. Without
// this, a user who saves with a password and later enables the keyring keeps
// a plaintext snapshot on disk — the exact thing the JSON copy promises not
// to hold (SEC-04).
func TestSaveConnectionsBackupRedactsPasswords(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	writeConns(t, Connection{Name: "plain", Driver: "mysql", Password: "s3cret"})

	if err := saveConnections([]Connection{{Name: "next", Driver: "mysql"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(connectionsPath() + ".bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Fatalf("backup leaked the plaintext password: %s", raw)
	}
	// The rest of the entry survives, so the list is still recoverable.
	if !strings.Contains(string(raw), `"plain"`) || !strings.Contains(string(raw), `"mysql"`) {
		t.Fatalf("backup lost the connection entry: %s", raw)
	}
}

// A backup whose previous generation cannot be parsed is not written at all:
// it could not be restored, and copying it verbatim is the leak path above.
func TestSaveConnectionsSkipsUnparseableBackup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FYNEQUERY_CONFIG_DIR", dir)
	if err := os.WriteFile(connectionsPath(), []byte(`{"not":"an array","password":"s3cret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveConnections([]Connection{{Name: "next", Driver: "mysql"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(connectionsPath() + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("an unparseable previous generation must not be backed up (err=%v)", err)
	}
}

// Three secrets live in a connection entry, not one: the database password,
// the SSH password and the SSH key passphrase. All three must be cleared from
// the backup — a bastion credential is as sensitive as the database one.
func TestSaveConnectionsBackupRedactsEverySecret(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	writeConns(t, Connection{
		Name:     "tunneled",
		Driver:   "mysql",
		Password: "db-secret",
		SSH: db.SSHConfig{
			Enabled:       true,
			Host:          "bastion",
			User:          "deploy",
			Password:      "ssh-secret",
			KeyPath:       "/keys/id_ed25519",
			KeyPassphrase: "key-secret",
		},
	})

	if err := saveConnections([]Connection{{Name: "next", Driver: "mysql"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(connectionsPath() + ".bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	for _, secret := range []string{"db-secret", "ssh-secret", "key-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("backup leaked %q: %s", secret, raw)
		}
	}
	// Non-secret connection identity survives, so the list is recoverable.
	for _, kept := range []string{`"tunneled"`, `"bastion"`, `"deploy"`, `"/keys/id_ed25519"`} {
		if !strings.Contains(string(raw), kept) {
			t.Errorf("backup lost %s: %s", kept, raw)
		}
	}
}
