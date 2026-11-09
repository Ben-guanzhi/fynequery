package main

import (
	"testing"

	"fynequery/proto"
)

// OpenVerify is generic and does not set any pool size; the SQLite plugin must
// pin `:memory:` to one connection itself. Without the pin a concurrent
// statement could borrow a second physical handle and see a different (empty)
// in-memory database — the table created moments earlier would be invisible.
// The pluginkit test dialect already did this by hand (pluginkit_test.go), which
// is exactly the shape the production path was missing.

func TestSQLiteOpenMemoryPinsPoolToOne(t *testing.T) {
	db, _, err := (sqliteDialect{}).Open(proto.ConnectParams{Database: ":memory:"})
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1 (:memory: is per-connection)", got)
	}
}

func TestSQLiteOpenMemoryViaHostPinsPoolToOne(t *testing.T) {
	// The DSN may arrive through the Host field when the Database box is empty.
	db, _, err := (sqliteDialect{}).Open(proto.ConnectParams{Host: ":memory:"})
	if err != nil {
		t.Fatalf("Open(Host=:memory:): %v", err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestSQLiteOpenFileDoesNotPinPool(t *testing.T) {
	db, _, err := (sqliteDialect{}).Open(proto.ConnectParams{Database: t.TempDir() + "/pool.db"})
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got == 1 {
		t.Errorf("file DB MaxOpenConnections = 1, want the unset default (only :memory: is pinned)")
	}
}
