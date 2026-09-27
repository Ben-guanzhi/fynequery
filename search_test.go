package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSearchObjects drives the quick-open engine against a real SQLite file:
// fragment matching, wildcard escaping, and the no-match path.
func TestSearchObjects(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	db, err := createConnection(ConnConfig{Type: dbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE user_roles (id INTEGER PRIMARY KEY, role TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, total REAL)`,
		`CREATE VIEW v_orders AS SELECT * FROM orders`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	db.Close()

	c := &Client{live: make(map[string]*Conn), fkCache: make(map[string][]ForeignKeyInfo)}
	if err := c.connect(ConnConfig{ID: "c0", Name: "t", Type: dbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })

	// A plain fragment finds tables and views, with the database filled in.
	hits, err := c.searchObjects("user", 0)
	if err != nil {
		t.Fatalf("searchObjects: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("searchObjects(user) = %d hits, want 2: %+v", len(hits), hits)
	}
	for _, h := range hits {
		if h.ConnID != "c0" || h.Database != dbFile || h.Type != "table" {
			t.Errorf("unexpected hit: %+v", h)
		}
	}

	// The empty path answers cleanly.
	if hits, err := c.searchObjects("no_such_table_xyz", 0); err != nil || len(hits) != 0 {
		t.Errorf("no-match search = %v, %v", hits, err)
	}
	// A blank pattern matches nothing rather than dumping the catalog.
	if hits, err := c.searchObjects("   ", 0); err != nil || len(hits) != 0 {
		t.Errorf("blank search = %v, %v", hits, err)
	}
	// A typed % is a literal, so "ord%" matches nothing even though orders exists.
	if hits, err := c.searchObjects("ord%", 0); err != nil || len(hits) != 0 {
		t.Errorf("escaped-wildcard search = %v, %v", hits, err)
	}
	// A view is found and typed as one.
	if hits, err := c.searchObjects("v_ord", 0); err != nil || len(hits) != 1 || hits[0].Type != "view" {
		t.Errorf("view search = %+v, %v", hits, err)
	}
}

// TestLikePattern checks the wildcard escaping of the quick-open pattern.
func TestLikePattern(t *testing.T) {
	cases := map[string]string{
		"abc":  "%abc%",
		"a_b":  `%a\_b%`,
		"100%": `%100\%%`,
		`a\b`:  `%a\\b%`,
	}
	for in, want := range cases {
		if got := likePattern(in); got != want {
			t.Errorf("likePattern(%q) = %q, want %q", in, got, want)
		}
	}
	if !strings.HasPrefix(likePattern("x"), "%") || !strings.HasSuffix(likePattern("x"), "%") {
		t.Error("pattern must be wrapped in wildcards")
	}
}
