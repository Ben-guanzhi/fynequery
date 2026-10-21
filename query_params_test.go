package main

// Auto-LIMIT regressions: a LIMIT on its own line must be detected (the old
// " limit " substring check missed it and appended a second LIMIT, which
// MySQL rejects with error 1064).

import "testing"

func TestAutoLimitAppend(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"limit on its own line", "SELECT *\nFROM users\nLIMIT 20;", "SELECT *\nFROM users\nLIMIT 20;"},
		{"limit inline", "SELECT * FROM users LIMIT 20", "SELECT * FROM users LIMIT 20"},
		{"offset inline", "SELECT * FROM users OFFSET 20", "SELECT * FROM users OFFSET 20"},
		{"plain select", "SELECT * FROM users", "SELECT * FROM users\nLIMIT 100"},
		{"semicolon stripped", "SELECT * FROM users;", "SELECT * FROM users\nLIMIT 100"},
		{"cte", "WITH x AS (SELECT 1) SELECT * FROM x", "WITH x AS (SELECT 1) SELECT * FROM x\nLIMIT 100"},
		{"limit inside string", "SELECT 'limit' AS w FROM t", "SELECT 'limit' AS w FROM t\nLIMIT 100"},
		{"quoted limit column", "SELECT `limit` FROM t", "SELECT `limit` FROM t\nLIMIT 100"},
		{"limit inside comment", "SELECT * FROM t -- limit 5\n", "SELECT * FROM t -- limit 5\nLIMIT 100"},
		{"leading comment", "-- pick\nSELECT * FROM t", "-- pick\nSELECT * FROM t\nLIMIT 100"},
		{"for update stays last", "SELECT * FROM t FOR UPDATE", "SELECT * FROM t FOR UPDATE"},
		{"for share stays last", "SELECT * FROM t FOR SHARE", "SELECT * FROM t FOR SHARE"},
		{"no key update stays last", "SELECT * FROM t FOR NO KEY UPDATE", "SELECT * FROM t FOR NO KEY UPDATE"},
		{"into outfile stays last", "SELECT * FROM t INTO OUTFILE '/tmp/x'", "SELECT * FROM t INTO OUTFILE '/tmp/x'"},
		{"insert untouched", "INSERT INTO t VALUES (1)", "INSERT INTO t VALUES (1)"},
		{"update untouched", "UPDATE t SET a = 1", "UPDATE t SET a = 1"},
		{"show untouched", "SHOW TABLES", "SHOW TABLES"},
		{"ddl untouched", "CREATE TABLE t (id INT)", "CREATE TABLE t (id INT)"},
	}
	for _, c := range cases {
		if got := autoLimit(c.in, "mysql", 100); got != c.want {
			t.Errorf("%s: autoLimit(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	if got := autoLimit("SELECT * FROM t", "mssql", 100); got != "SELECT * FROM t" {
		t.Errorf("mssql must be untouched, got %q", got)
	}
	if got := autoLimit("SELECT * FROM t", "mysql", 0); got != "SELECT * FROM t" {
		t.Errorf("n=0 must be untouched, got %q", got)
	}
}
