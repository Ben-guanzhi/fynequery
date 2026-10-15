package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestTableIndexesSQLite drives the index reader against a real SQLite file:
// declared indexes come back with their columns, implicit autoindexes do not.
func TestTableIndexesSQLite(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	db, err := createConnection(ConnConfig{Type: DbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT UNIQUE)`,
		`CREATE INDEX idx_users_name ON users(name)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	db.Close()

	c := &Store{live: make(map[string]*Conn)}
	if err := c.Connect(ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect("c0") })

	indexes, err := c.TableIndexes(context.Background(), "c0", dbFile, "", "users")
	if err != nil {
		t.Fatalf("tableIndexes: %v", err)
	}
	if len(indexes) != 1 {
		t.Fatalf("indexes = %+v, want only idx_users_name", indexes)
	}
	idx := indexes[0]
	if idx.Name != "idx_users_name" || idx.Unique || idx.Primary {
		t.Errorf("index = %+v, want non-unique idx_users_name", idx)
	}
	if len(idx.Columns) != 1 || idx.Columns[0] != "name" {
		t.Errorf("index columns = %v, want [name]", idx.Columns)
	}
}

// TestAggregateIndexes checks the flat-to-structured fold, including the
// multi-column case keeping catalog order.
func TestAggregateIndexes(t *testing.T) {
	indexes := aggregateIndexes([]indexRow{
		{name: "idx_ab", kind: "BTREE", column: "a", unique: true},
		{name: "idx_ab", kind: "BTREE", column: "b", unique: true},
		{name: "idx_c", kind: "HASH", column: "c"},
	})
	if len(indexes) != 2 {
		t.Fatalf("indexes = %d, want 2", len(indexes))
	}
	if indexes[0].Name != "idx_ab" || !indexes[0].Unique || len(indexes[0].Columns) != 2 ||
		indexes[0].Columns[0] != "a" || indexes[0].Columns[1] != "b" {
		t.Errorf("aggregated index = %+v", indexes[0])
	}
	if indexes[1].Kind != "HASH" || indexes[1].Unique {
		t.Errorf("second index = %+v", indexes[1])
	}
}
