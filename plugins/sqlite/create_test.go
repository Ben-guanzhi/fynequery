package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateDatabaseFile verifies the SQLite dialect CreateDatabase actually
// materializes a database file at the given path (it is otherwise a no-op
// connection-wise).
func TestCreateDatabaseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")

	if err := (sqliteDialect{}).CreateDatabase(nil, path, "", ""); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected database file %s to exist: %v", path, err)
	}
}

// TestCreateDatabaseRejectsEmptyName guards the SQLite path validation.
func TestCreateDatabaseRejectsEmptyName(t *testing.T) {
	if err := (sqliteDialect{}).CreateDatabase(nil, "", "", ""); err == nil {
		t.Fatal("expected error for empty database file path")
	}
}

// TestCreateDatabaseMemory is a smoke test that ":memory:" is accepted without
// trying to create a real file.
func TestCreateDatabaseMemory(t *testing.T) {
	if err := (sqliteDialect{}).CreateDatabase(nil, ":memory:", "", ""); err != nil {
		t.Fatalf("CreateDatabase :memory: %v", err)
	}
}
