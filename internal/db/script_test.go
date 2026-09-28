package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   []string
	}{
		{"two statements", "SELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"trailing semicolon", "SELECT 1;", []string{"SELECT 1"}},
		{"whitespace trimmed", "  SELECT 1 ;  ", []string{"SELECT 1"}},
		{"string keeps its semicolon", `SELECT ';'; SELECT 2`, []string{`SELECT ';'`, "SELECT 2"}},
		{"escaped quote stays", `SELECT 'It''s; fine'; SELECT 2`, []string{`SELECT 'It''s; fine'`, "SELECT 2"}},
		{"line comment hides semicolon", "SELECT 1 -- ; not a split\n; SELECT 2", []string{"SELECT 1 -- ; not a split", "SELECT 2"}},
		{"block comment hides semicolon", "/* ; */ SELECT 1", []string{"/* ; */ SELECT 1"}},
		{"quoted identifier hides semicolon", "SELECT \"a;b\" FROM t", []string{`SELECT "a;b" FROM t`}},
		{"comment only yields nothing", "-- hello\n/* ; */", nil},
		{"empty script", "  ", nil},
	}
	for _, tc := range cases {
		got := SplitStatements(tc.script)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %d statements %v, want %d", tc.name, len(got), got, len(tc.want))
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%s: statement %d = %q, want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func TestRunScript(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	db, err := createConnection(ConnConfig{Type: DbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	c := &Store{
		live:        make(map[string]*Conn),
		historyFile: filepath.Join(dir, "history.json"),
	}
	if err := c.Connect(ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect("c0") })
	conn := c.live["c0"]

	stmts := SplitStatements("INSERT INTO t (name) VALUES ('a'); INSERT INTO t (name) VALUES ('b'); SELECT id, name FROM t ORDER BY id")
	last, lastStmt, ok := c.RunScript(context.Background(), conn, dbFile, stmts, 0)
	if ok != 3 || last.Error != "" {
		t.Fatalf("script ok = %d, last.Error = %q", ok, last.Error)
	}
	if last.RowCount != 2 || lastStmt != stmts[2] {
		t.Fatalf("last result rows = %d, lastStmt = %q", last.RowCount, lastStmt)
	}

	// A failing statement stops the run: the count reports progress and the
	// failed statement comes back as "last".
	bad := SplitStatements("INSERT INTO t (name) VALUES ('c'); UPDATE no_such_table SET x = 1; SELECT 1")
	last, lastStmt, ok = c.RunScript(context.Background(), conn, dbFile, bad, 0)
	if ok != 1 || last.Error == "" || lastStmt != bad[1] {
		t.Fatalf("failing script: ok = %d, last.Error = %q, lastStmt = %q", ok, last.Error, lastStmt)
	}
}

func TestNormalizeDefault(t *testing.T) {
	if got := normalizeDefault(DbTypeSQLServer, "((0))"); got != "0" {
		t.Errorf("normalizeDefault = %q, want 0", got)
	}
	if got := normalizeDefault(DbTypeMySQL, "('x')"); got != "('x')" {
		t.Errorf("non-mssql default should stay untouched, got %q", got)
	}
}
