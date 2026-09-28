package db

import (
	"os"
	"path/filepath"
	"testing"
)

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
