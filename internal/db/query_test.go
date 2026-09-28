package db

import (
	"testing"
)

func TestWithRowLimit(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		dbType string
		limit  int
		want   string
	}{
		{"mysql plain", "SELECT id FROM t", DbTypeMySQL, 100, "SELECT id FROM t LIMIT 100"},
		{"trailing semicolon", "SELECT id FROM t;", DbTypeMySQL, 100, "SELECT id FROM t LIMIT 100"},
		{"already limited", "SELECT id FROM t LIMIT 5", DbTypeMySQL, 100, "SELECT id FROM t LIMIT 5"},
		{"already has top", "SELECT TOP 5 id FROM t", DbTypeSQLServer, 100, "SELECT TOP 5 id FROM t"},
		{"mssql top", "SELECT id FROM t", DbTypeSQLServer, 100, "SELECT TOP (100) id FROM t"},
		{"oracle rownum", "SELECT id FROM t", DbTypeOracle, 100, "SELECT * FROM (SELECT id FROM t) WHERE ROWNUM <= 100"},
		{"cte gets limit", "WITH x AS (SELECT id FROM t) SELECT * FROM x", DbTypePostgres, 100, "WITH x AS (SELECT id FROM t) SELECT * FROM x LIMIT 100"},
		{"oracle cte wrapped", "WITH x AS (SELECT id FROM t) SELECT * FROM x", DbTypeOracle, 10, "SELECT * FROM (WITH x AS (SELECT id FROM t) SELECT * FROM x) WHERE ROWNUM <= 10"},
		{"mssql cte untouched", "WITH x AS (SELECT id FROM t) SELECT * FROM x", DbTypeSQLServer, 100, "WITH x AS (SELECT id FROM t) SELECT * FROM x"},
		{"zero limit no-op", "SELECT id FROM t", DbTypeMySQL, 0, "SELECT id FROM t"},
		{"non select no-op", "UPDATE t SET a = 1", DbTypeMySQL, 100, "UPDATE t SET a = 1"},
	}
	for _, tc := range cases {
		if got := withRowLimit(tc.query, tc.dbType, tc.limit); got != tc.want {
			t.Errorf("%s: withRowLimit(%q, %q, %d) = %q, want %q",
				tc.name, tc.query, tc.dbType, tc.limit, got, tc.want)
		}
	}
}

func TestIsSelectQuery(t *testing.T) {
	selects := []string{
		"SELECT 1", "  select 1", "WITH x AS (SELECT 1) SELECT * FROM x",
		"SHOW DATABASES", "DESCRIBE t", "EXPLAIN SELECT 1",
	}
	for _, q := range selects {
		if !isSelectQuery(q) {
			t.Errorf("isSelectQuery(%q) = false, want true", q)
		}
	}
	nonSelects := []string{"UPDATE t SET a = 1", "INSERT INTO t VALUES (1)", "DROP TABLE t"}
	for _, q := range nonSelects {
		if isSelectQuery(q) {
			t.Errorf("isSelectQuery(%q) = true, want false", q)
		}
	}
	// WITH is routed through runSelect unconditionally: both common CTE forms
	// (plain WITH ... SELECT and data-modifying WITH ... SELECT ... RETURNING)
	// end in a resultset, so db.Query is the right channel even for the rare
	// bare writable CTE, which still executes there.
}

func TestNextConnSeq(t *testing.T) {
	if got := nextConnSeq([]ConnConfig{{ID: "c1"}, {ID: "c2"}}); got != 3 {
		t.Fatalf("nextConnSeq(c1,c2) = %d, want 3", got)
	}
	if got := nextConnSeq(nil); got != 0 {
		t.Fatalf("nextConnSeq(nil) = %d, want 0", got)
	}
	if got := nextConnSeq([]ConnConfig{{ID: "weird"}}); got != 0 {
		t.Fatalf("nextConnSeq(non-numeric) = %d, want 0", got)
	}
}
