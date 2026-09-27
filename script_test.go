package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
)

// TestSplitStatements checks the script lexer: semicolons inside strings,
// quoted identifiers and comments do not split; blank and comment-only
// chunks are dropped.
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
		got := splitStatements(tc.script)
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

// TestRunScript drives the sequential executor against a real SQLite file:
// every statement lands, the last SELECT becomes the result, and a failure
// stops the script with its position reported.
func TestRunScript(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	db, err := createConnection(ConnConfig{Type: dbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	c := &Client{
		live: make(map[string]*Conn), treeNodes: make(map[string]*nodeData),
		expanded: make(map[string]bool), sugTables: make(map[string][]string),
		sugLoading: make(map[string]bool), fkCache: make(map[string][]ForeignKeyInfo),
		edits: newGridEditState(), sortCol: -1, historyFile: filepath.Join(dir, "history.json"),
	}
	if err := c.connect(ConnConfig{ID: "c0", Name: "t", Type: dbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })
	conn := c.live["c0"]

	stmts := splitStatements("INSERT INTO t (name) VALUES ('a'); INSERT INTO t (name) VALUES ('b'); SELECT id, name FROM t ORDER BY id")
	last, lastStmt, ok := c.runScript(context.Background(), conn, dbFile, stmts, 0)
	if ok != 3 || last.Error != "" {
		t.Fatalf("script ok = %d, last.Error = %q", ok, last.Error)
	}
	if last.RowCount != 2 || lastStmt != stmts[2] {
		t.Fatalf("last result rows = %d, lastStmt = %q", last.RowCount, lastStmt)
	}

	// A failing statement stops the run: the count reports progress and the
	// failed statement comes back as "last".
	bad := splitStatements("INSERT INTO t (name) VALUES ('c'); UPDATE no_such_table SET x = 1; SELECT 1")
	last, lastStmt, ok = c.runScript(context.Background(), conn, dbFile, bad, 0)
	if ok != 1 || last.Error == "" || lastStmt != bad[1] {
		t.Fatalf("failing script: ok = %d, last.Error = %q, lastStmt = %q", ok, last.Error, lastStmt)
	}
}

// TestBuildCreateTableSQLite reconstructs the DDL of a real SQLite table:
// the primary key survives even though SQLite hides it in an autoindex.
func TestBuildCreateTableSQLite(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	db, err := createConnection(ConnConfig{Type: dbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE INDEX idx_users_name ON users(name)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	db.Close()

	c := &Client{
		live: make(map[string]*Conn), treeNodes: make(map[string]*nodeData),
		expanded: make(map[string]bool), sugTables: make(map[string][]string),
		sugLoading: make(map[string]bool), fkCache: make(map[string][]ForeignKeyInfo),
		edits: newGridEditState(), sortCol: -1,
	}
	if err := c.connect(ConnConfig{ID: "c0", Name: "t", Type: dbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })
	c.buildSchemaView() // renders navigator rows for the assertions below

	// Connecting auto-expands the connection's own database in the navigator.
	c.refreshSchema()
	if !c.expanded["db:"+dbFile] {
		t.Fatalf("default database was not auto-expanded")
	}

	// A failed load attaches a red retryable error row; retrying reloads.
	c.addErrorNode(c.treeNodes["db:"+dbFile], errors.New("boom"))
	c.rebuildSchemaRows()
	foundErr := false
	for _, obj := range c.schemaRows.Objects {
		if btn := schemaRowButtonOf(obj); btn != nil && strings.Contains(btn.Text, "boom") {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatalf("error row not rendered")
	}
	c.retrySchemaNode("db:" + dbFile)
	for _, ch := range c.treeNodes["db:"+dbFile].children {
		if n := c.treeNodes[ch]; n != nil && n.kind == "error" {
			t.Fatalf("error node survived retry")
		}
	}
	if len(c.treeNodes["db:"+dbFile].children) == 0 {
		t.Fatal("retry did not reload tables")
	}

	ddl, err := c.tableDDL("c0", dbFile, "", "users")
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

// TestNormalizeDefault checks the MSSQL parenthesis unwrapping.
func TestNormalizeDefault(t *testing.T) {
	if got := normalizeDefault(dbTypeSQLServer, "((0))"); got != "0" {
		t.Errorf("normalizeDefault = %q, want 0", got)
	}
	if got := normalizeDefault(dbTypeMySQL, "('x')"); got != "('x')" {
		t.Errorf("non-mssql default should stay untouched, got %q", got)
	}
}

// TestEmptyDatabaseShowsDiagnosticNode: connecting to a database with no
// tables renders an explanatory node instead of a silent blank expansion.
func TestEmptyDatabaseShowsDiagnosticNode(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "empty.db")

	db, err := createConnection(ConnConfig{Type: dbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	db.Close()
	c.historyFile = filepath.Join(dir, "history.json")

	if err := c.connect(ConnConfig{ID: "c0", Name: "t", Type: dbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })
	c.buildSchemaView()
	c.refreshSchema()

	// The auto-expand of the default database attaches the diagnostic node.
	found := false
	for _, ch := range c.treeNodes["db:"+dbFile].children {
		if n := c.treeNodes[ch]; n != nil && n.kind == "empty" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no diagnostic node for an empty database")
	}

	// Switching the database dropdown re-loads and re-expands that db.
	c.expandDatabaseInTree(dbFile)
	if !c.expanded["db:"+dbFile] {
		t.Fatal("dropdown switch did not expand the database")
	}
}

// TestSuggestEntryGrowsWithLines locks the line-number design: the editor's
// MinSize grows with its text so a shared scroller can carry editor and
// gutter together.
func TestSuggestEntryGrowsWithLines(t *testing.T) {
	test.NewApp()
	e := newSuggestEntry()
	e.SetText("line1")
	h1 := e.MinSize().Height
	for i := 2; i <= 10; i++ {
		e.SetText(e.Text + "\n" + "line" + string(rune('0'+i)))
	}
	h10 := e.MinSize().Height
	if h10 <= h1 {
		t.Fatalf("editor MinSize did not grow: %v -> %v", h1, h10)
	}
}
