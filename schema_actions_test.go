package main

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

func TestBuildCreateTableSQLite(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	sdb, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE INDEX idx_users_name ON users(name)`,
	} {
		if _, err := sdb.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	sdb.Close()

	c := &Client{
		store: db.NewStore(),
		tree:  treeState{nodes: make(map[string]*nodeData), expanded: make(map[string]bool)},
		cache: memoCache{tables: make(map[string][]string), objects: make(map[string][]string), loading: make(map[string]bool), loadingObjects: make(map[string]bool), fks: make(map[string][]db.ForeignKeyInfo)},
		grid:  gridState{edits: newGridEditState(), sortCol: -1},
	}
	if err := c.connect(db.ConnConfig{ID: "c0", Name: "t", Type: db.DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })
	c.buildSchemaView() // renders navigator rows for the assertions below

	// Connecting auto-expands the connection's own database in the navigator.
	c.refreshSchema()
	if !c.tree.expanded["db:"+dbFile] {
		t.Fatalf("default database was not auto-expanded")
	}

	// A failed load attaches a red retryable error row; retrying reloads.
	c.addErrorNode(c.tree.nodes["db:"+dbFile], errors.New("boom"))
	c.rebuildSchemaRows()
	foundErr := false
	for _, obj := range c.tree.rows.Objects {
		if btn := schemaRowButtonOf(obj); btn != nil && strings.Contains(btn.Text, "boom") {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatalf("error row not rendered")
	}
	c.retrySchemaNode("db:" + dbFile)
	for _, ch := range c.tree.nodes["db:"+dbFile].children {
		if n := c.tree.nodes[ch]; n != nil && n.kind == "error" {
			t.Fatalf("error node survived retry")
		}
	}
	if len(c.tree.nodes["db:"+dbFile].children) == 0 {
		t.Fatal("retry did not reload tables")
	}

	ddl, err := c.store.TableDDL("c0", dbFile, "", "users")
	if err != nil {
		t.Fatalf("tableDDL: %v", err)
	}
	for _, want := range []string{
		`CREATE TABLE "users" (`,
		`"id" INTEGER`,
		`"name" TEXT NOT NULL`,
		`PRIMARY KEY ("id")`,
		`CREATE INDEX "idx_users_name" ON "users" ("name");`,
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL missing %q:\n%s", want, ddl)
		}
	}
}

func TestEmptyDatabaseShowsDiagnosticNode(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "empty.db")

	sdb, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sdb.Close()

	if err := c.connect(db.ConnConfig{ID: "c0", Name: "t", Type: db.DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })
	c.buildSchemaView()
	c.refreshSchema()

	// The auto-expand of the default database attaches the diagnostic node.
	found := false
	for _, ch := range c.tree.nodes["db:"+dbFile].children {
		if n := c.tree.nodes[ch]; n != nil && n.kind == "empty" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no diagnostic node for an empty database")
	}

	// Switching the database dropdown re-loads and re-expands that db.
	c.expandDatabaseInTree(dbFile)
	if !c.tree.expanded["db:"+dbFile] {
		t.Fatal("dropdown switch did not expand the database")
	}
}
