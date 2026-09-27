package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2/test"
)

// TestEndToEndSQLite drives the full core pipeline against a real SQLite file:
// connect, execute SELECT, and browse databases / tables / columns. This proves
// the DSN, pooling and query paths all work together (no GUI required).
func TestEndToEndSQLite(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	db, err := createConnection(ConnConfig{Type: dbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	// Seed a table and some rows directly.
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, active INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (name, active) VALUES ('alice', 1), ('bob', 0)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()

	c := &Client{live: make(map[string]*Conn), saved: []ConnConfig{
		{ID: "c0", Type: dbTypeSQLite, Database: dbFile},
	}}

	cfg := c.saved[0]
	if err := c.connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect(cfg.ID) })

	// Execute a SELECT.
	res := c.execute(context.Background(), c.live[cfg.ID], "", "SELECT id, name, active FROM users ORDER BY id", 100)
	if res.Error != "" {
		t.Fatalf("execute: %s", res.Error)
	}
	if res.RowCount != 2 {
		t.Fatalf("expected 2 rows, got %d", res.RowCount)
	}
	if len(res.Columns) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(res.Columns))
	}
	if res.Rows[0][1] != "alice" {
		t.Fatalf("expected first row name alice, got %v", res.Rows[0][1])
	}

	// Row limit wrapper applies to plain SELECTs.
	limited := c.execute(context.Background(), c.live[cfg.ID], "", "SELECT * FROM users", 1)
	if limited.RowCount != 1 {
		t.Fatalf("row limit: expected 1 row, got %d", limited.RowCount)
	}

	// Non-SELECT command reports affected rows.
	ins := c.execute(context.Background(), c.live[cfg.ID], "", "INSERT INTO users (name, active) VALUES ('carol', 1)", 0)
	if ins.Error != "" {
		t.Fatalf("insert exec: %s", ins.Error)
	}
	if ins.AffectedRows != 1 {
		t.Fatalf("expected 1 affected row, got %d", ins.AffectedRows)
	}

	// Schema browsing.
	dbs, err := c.databases(cfg.ID)
	if err != nil || len(dbs) != 1 {
		t.Fatalf("databases: %v (len=%d)", err, len(dbs))
	}
	if dbs[0] != dbFile {
		t.Fatalf("expected database %q, got %q", dbFile, dbs[0])
	}

	tables, err := c.tables(cfg.ID, dbFile, "")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "users" {
		t.Fatalf("expected users table, got %+v", tables)
	}

	cols, err := c.columns(cfg.ID, dbFile, "", "users")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(cols))
	}
	var sawPK bool
	for _, col := range cols {
		if col.Name == "id" && col.IsPrimaryKey {
			sawPK = true
		}
	}
	if !sawPK {
		t.Fatalf("expected id to be a primary key, got %+v", cols)
	}
}

// TestConnectionsFileRoundTrip verifies the DPAPI-less code path persists and
// reloads connections without losing the password (plaintext on non-Windows;
// sealed on Windows).
func TestConnectionsFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "connections.json")
	c := &Client{live: make(map[string]*Conn), connFile: file}
	c.saved = []ConnConfig{{ID: "c0", Name: "t", Type: dbTypeSQLite, Database: ":memory:", Password: "pw"}}
	c.persist()

	// Re-load into a fresh client (same file, unsealed at load).
	c2 := &Client{live: make(map[string]*Conn), connFile: file}
	c2.loadConnections()
	if len(c2.saved) != 1 {
		t.Fatalf("expected 1 connection after reload, got %d", len(c2.saved))
	}
	if c2.saved[0].Password != "pw" {
		t.Fatalf("password not preserved, got %q", c2.saved[0].Password)
	}
	_ = os.Remove(file)
}

// TestQueryCancel proves the cancellation plumbing: an already-cancelled
// context makes execute report a cancellation error, which is what the
// cancel button produces for a long-running query.
func TestQueryCancel(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("")
	c := newTestClient(a, w)
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "t.db")

	db, err := createConnection(ConnConfig{Type: dbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (name) VALUES ('a')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()
	c.historyFile = filepath.Join(dir, "h.json")

	if err := c.connect(ConnConfig{ID: "c0", Name: "t", Type: dbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })

	conn, err := c.openConn("c0")
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := c.execute(ctx, conn, "", "SELECT name FROM users", 0)
	if res.Error == "" {
		t.Fatal("expected a cancellation error, got none")
	}
}
