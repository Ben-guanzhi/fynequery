package db

import (
	"context"
	"path/filepath"
	"strings"
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

// TestSplitStatementsSpansPinsEachStatementToItsOwnText checks the offsets
// SplitStatements throws away: Start must point at exactly the first byte of
// the text it hands back, so a caller can trust that Text lives at
// script[Start:] and re-slice the original from there.
func TestSplitStatementsSpansPinsEachStatementToItsOwnText(t *testing.T) {
	script := "\n  SELECT 1 ;\n\tSELECT 'a;b'\n;\n-- stray comment\nSELECT 3"
	spans := SplitStatementsSpans(script)
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3: %+v", len(spans), spans)
	}
	for _, s := range spans {
		if got := script[s.Start:]; len(got) < len(s.Text) || got[:len(s.Text)] != s.Text {
			t.Errorf("span %+v does not start at its own text: script[%d:] = %q", s, s.Start, got)
		}
		if s.End < s.Start+len(s.Text) {
			t.Errorf("span %+v ends before its text does", s)
		}
		if s.End > len(script) {
			t.Errorf("span %+v ends past the script", s)
		}
	}
	// The semicolon belongs to the statement it terminates, and nothing after
	// it does.
	if spans[0].End != len("\n  SELECT 1 ;") || script[spans[0].End-1] != ';' {
		t.Errorf("first span = %+v, want its End just past the semicolon", spans[0])
	}
}

// TestStmtAtIsUnaffectedBySemicolonsThatDoNotSplit pins why this cannot be
// done by searching for semicolons in the UI layer: a semicolon inside a
// string, a line comment or a block comment is not a split, so the caret sitting
// on one of them is still inside the statement that holds it.
func TestStmtAtIsUnaffectedBySemicolonsThatDoNotSplit(t *testing.T) {
	const script = "SELECT ';', /* ; */ 1 -- ; trailing\nFROM t;\n\nSELECT 2"
	const first = "SELECT ';', /* ; */ 1 -- ; trailing\nFROM t"
	caret := func(marker string, skip int) int {
		i := strings.Index(script, marker)
		if i < 0 {
			t.Fatalf("caret marker %q not found in the script", marker)
		}
		return i + skip
	}
	cases := []struct {
		name string
		off  int
		want string
	}{
		{"inside a string literal", caret("';'", 1), first},
		{"inside a block comment", caret("/* ; */", 4), first},
		{"inside a line comment", caret("-- ;", 3), first},
		{"on the semicolon that does split", caret("t;", 1), first},
		{"in the blank run after it", caret("t;\n\n", 3), first},
		{"inside the second statement", caret("SELECT 2", 3), "SELECT 2"},
	}
	for _, tc := range cases {
		got, ok := StmtAt(script, tc.off)
		if !ok {
			t.Errorf("%s: StmtAt at %d found nothing", tc.name, tc.off)
			continue
		}
		if got.Text != tc.want {
			t.Errorf("%s: StmtAt(%d) = %q, want %q", tc.name, tc.off, got.Text, tc.want)
		}
	}
}

func TestStmtAtEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		script  string
		off     int
		wantOK  bool
		wantTxt string
	}{
		{"empty script has nothing to answer", "  ", 0, false, ""},
		{"comments only has nothing to answer", "-- hello\n/* ; */", 2, false, ""},
		// Comments that sit above a statement belong to it - SplitStatements
		// has always handed them over as part of it - so there is nothing to
		// fall forward to here.
		{"leading comment belongs to the statement", "-- pick me\nSELECT 1", 0, true, "-- pick me\nSELECT 1"},
		{"leading blank falls forward", "\n\nSELECT 1", 0, true, "SELECT 1"},
		{"caret past the end falls back to the last", "SELECT 1; SELECT 2", 100, true, "SELECT 2"},
		{"unterminated literal is one statement", "SELECT 'abc", 9, true, "SELECT 'abc"},
	}
	for _, tc := range cases {
		got, ok := StmtAt(tc.script, tc.off)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if ok && got.Text != tc.wantTxt {
			t.Errorf("%s: StmtAt = %q, want %q", tc.name, got.Text, tc.wantTxt)
		}
	}
}

// TestStmtsInRangeSnapsToWholeStatements is why selecting half a statement still
// runs all of it: there is no meaningful answer to "run the middle of a
// WHERE clause".
func TestStmtsInRangeSnapsToWholeStatements(t *testing.T) {
	const script = "SELECT 1;\nSELECT 2;\nSELECT 3"
	whole := StmtsInRange(script, 0, len(script))
	if len(whole) != 3 {
		t.Fatalf("whole script = %d statements, want 3", len(whole))
	}
	middle := StmtsInRange(script, strings.Index(script, "LECT 2"), strings.Index(script, "LECT 2")+5)
	if len(middle) != 1 || middle[0].Text != "SELECT 2" {
		t.Fatalf("a word inside statement 2 = %+v, want the whole of it", middle)
	}
	straddle := StmtsInRange(script, strings.Index(script, "LECT 2"), len(script))
	if len(straddle) != 2 || straddle[0].Text != "SELECT 2" || straddle[1].Text != "SELECT 3" {
		t.Fatalf("a selection from 2 into 3 = %+v", straddle)
	}
	if got := StmtsInRange(script, 5, 2); len(got) != 1 {
		t.Fatalf("a reversed selection should mean the same range, got %+v", got)
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
