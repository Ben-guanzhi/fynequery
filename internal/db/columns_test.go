package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestSQLiteColumnFlags reads one table's metadata from a real SQLite file and
// pins the two scanner decisions: NOT NULL versus nullable, and membership in a
// composite primary key.
//
// The key check matters beyond the structure view: the grid builds a row's
// UPDATE/DELETE WHERE from IsPrimaryKey, so a composite key that loses its tail
// columns would under-constrain the write and hit neighbouring rows.
func TestSQLiteColumnFlags(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "cols.db")

	db, err := createConnection(ConnConfig{Type: DbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE comp (a INTEGER, b INTEGER, c INTEGER NOT NULL, name TEXT, PRIMARY KEY (a, b))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()

	c := &Store{live: make(map[string]*Conn)}
	if err := c.Connect(ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect("c0") })

	cols, err := c.Columns(context.Background(), "c0", dbFile, "", "comp")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	var pk []string
	nullable := map[string]bool{}
	for _, col := range cols {
		if col.IsPrimaryKey {
			pk = append(pk, col.Name)
		}
		nullable[col.Name] = col.Nullable
	}
	if got := strings.Join(pk, ","); got != "a,b" {
		t.Fatalf("primary key columns = %q, want a,b", got)
	}
	if nullable["c"] {
		t.Error("c is NOT NULL in the DDL but reported nullable")
	}
	if !nullable["name"] {
		t.Error("name should be nullable")
	}
}
