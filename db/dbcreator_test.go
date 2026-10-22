package db

import (
	"testing"
)

// TestBuildCreateDatabaseSQL verifies the MySQL CREATE DATABASE statement
// builder: defaults, quoting, optional collation, and injection guards.
func TestBuildCreateDatabaseSQL(t *testing.T) {
	cases := []struct {
		label  string
		dbname string
		opts   map[string]string
		want   string
		werr   bool
	}{
		{"defaults utf8mb4", "shop", nil,
			"CREATE DATABASE `shop` CHARACTER SET utf8mb4", false},
		{"explicit charset", "shop", map[string]string{"charset": "latin1"},
			"CREATE DATABASE `shop` CHARACTER SET latin1", false},
		{"charset + collation", "shop", map[string]string{"charset": "utf8mb4", "collation": "utf8mb4_bin"},
			"CREATE DATABASE `shop` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", false},
		{"identifier with backtick rejected", "a`b", nil, "", true},
		{"empty name rejected", "", nil, "", true},
		{"name with space rejected", "bad name", nil, "", true},
		{"invalid charset rejected", "shop", map[string]string{"charset": "utf8; DROP"}, "", true},
		{"invalid collation rejected", "shop", map[string]string{"collation": "x`y"}, "", true},
	}
	for _, c := range cases {
		got, err := buildCreateDatabaseSQL(c.dbname, c.opts)
		if c.werr {
			if err == nil {
				t.Errorf("%s: expected error, got nil (sql=%q)", c.label, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.label, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.label, got, c.want)
		}
	}
}

// TestMysqlSessionImplementsCreator checks the built-in MySQL session satisfies
// the optional DatabaseCreator interface so the UI offers in-app creation.
func TestMysqlSessionImplementsCreator(t *testing.T) {
	var s Session = &mysqlSession{}
	if _, ok := s.(DatabaseCreator); !ok {
		t.Fatal("mysqlSession must implement DatabaseCreator")
	}
}

// TestCanCreateDatabaseFalseForNil guards that the capability probe is safe on a
// nil session and returns false.
func TestCanCreateDatabaseFalseForNil(t *testing.T) {
	if CanCreateDatabase(nil) {
		t.Fatal("nil session must not claim database creation")
	}
}
