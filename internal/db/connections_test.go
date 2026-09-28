package db

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestConnectionsFileStaysPrivate covers the promise behind the connection
// dialog's plain-text warning: the password file is readable only by its owner.
// WriteFile's mode only applies to a file it creates, so persist has to set the
// bits again over one another tool left loose.
func TestConnectionsFileStaysPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX permission bits to assert")
	}
	file := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(file, []byte("world readable"), 0666); err != nil {
		t.Fatalf("pre-create: %v", err)
	}
	s := &Store{live: make(map[string]*Conn), connFile: file}
	s.saved = []ConnConfig{{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: ":memory:", Password: "pw"}}
	s.persist()

	fi, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600 for a file that may hold a plain-text password", fi.Mode().Perm())
	}
}

// TestConnectionsFileRoundTrip verifies the DPAPI-less code path persists and
// reloads connections without losing the password (plaintext on non-Windows;
// sealed on Windows).
func TestConnectionsFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "connections.json")
	s := &Store{live: make(map[string]*Conn), connFile: file}
	s.saved = []ConnConfig{{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: ":memory:", Password: "pw"}}
	s.persist()

	// Re-load into a fresh store (same file, unsealed at load).
	s2 := &Store{live: make(map[string]*Conn), connFile: file}
	s2.loadConnections()
	if len(s2.saved) != 1 {
		t.Fatalf("expected 1 connection after reload, got %d", len(s2.saved))
	}
	if s2.saved[0].Password != "pw" {
		t.Fatalf("password not preserved, got %q", s2.saved[0].Password)
	}
	_ = os.Remove(file)
}
