package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

func newTestEntry() *suggestEntry {
	test.NewApp()
	return newSuggestEntry()
}

// TestMatchSuggestions checks prefix filtering: case-insensitive, exact
// matches dropped, deduplicated, capped.
func TestMatchSuggestions(t *testing.T) {
	pool := []string{"SELECT", "SET", "users", "USER_ROLES", "sessions", "SELECT", ""}
	got := matchSuggestions(pool, "se")
	// Sort is bytewise: capitals sort before lowercase.
	want := []string{"SELECT", "SET", "sessions"}
	if len(got) != len(want) {
		t.Fatalf("matchSuggestions(se) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("matchSuggestions(se) = %v, want %v", got, want)
		}
	}
	if got := matchSuggestions(pool, "select"); len(got) != 0 {
		t.Fatalf("exact match kept: %v", got)
	}
	// Cap: pool has many "a" words, result must not exceed maxSuggestions.
	big := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"}
	if got := matchSuggestions(big, "a"); len(got) != maxSuggestions {
		t.Fatalf("cap = %d, want %d", len(got), maxSuggestions)
	}
}

// TestWordSplice drives currentWord/replaceWord through a real editor: the
// identifier at the caret is replaced by the accepted suggestion and the
// caret lands after it.
func TestWordSplice(t *testing.T) {
	s := newTestEntry()
	s.SetText("SELECT * FR")
	s.CursorRow, s.CursorColumn = 0, 11 // end of "FR"

	w := s.currentWord()
	if w.text != "FR" || w.start != 9 || w.end != 11 {
		t.Fatalf("currentWord = %+v, want text FR span [9,11)", w)
	}
	s.suggest = []string{"FROM"}
	s.acceptSuggestion()
	if got := s.Entry.Text; got != "SELECT * FROM" {
		t.Fatalf("text after accept = %q", got)
	}
	if s.Entry.CursorColumn != 13 {
		t.Fatalf("cursor after accept = %d, want 13", s.Entry.CursorColumn)
	}
	if len(s.suggest) != 0 {
		t.Fatalf("suggestions not cleared after accept")
	}
}

// TestWithRowLimit verifies the row-cap wrapper: per-engine syntax, CTEs,
// queries that already declare a limit, and the no-op cases.
func TestWithRowLimit(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		dbType string
		limit  int
		want   string
	}{
		{"mysql plain", "SELECT id FROM t", dbTypeMySQL, 100, "SELECT id FROM t LIMIT 100"},
		{"trailing semicolon", "SELECT id FROM t;", dbTypeMySQL, 100, "SELECT id FROM t LIMIT 100"},
		{"already limited", "SELECT id FROM t LIMIT 5", dbTypeMySQL, 100, "SELECT id FROM t LIMIT 5"},
		{"already has top", "SELECT TOP 5 id FROM t", dbTypeSQLServer, 100, "SELECT TOP 5 id FROM t"},
		{"mssql top", "SELECT id FROM t", dbTypeSQLServer, 100, "SELECT TOP (100) id FROM t"},
		{"oracle rownum", "SELECT id FROM t", dbTypeOracle, 100, "SELECT * FROM (SELECT id FROM t) WHERE ROWNUM <= 100"},
		{"cte gets limit", "WITH x AS (SELECT id FROM t) SELECT * FROM x", dbTypePostgres, 100, "WITH x AS (SELECT id FROM t) SELECT * FROM x LIMIT 100"},
		{"oracle cte wrapped", "WITH x AS (SELECT id FROM t) SELECT * FROM x", dbTypeOracle, 10, "SELECT * FROM (WITH x AS (SELECT id FROM t) SELECT * FROM x) WHERE ROWNUM <= 10"},
		{"mssql cte untouched", "WITH x AS (SELECT id FROM t) SELECT * FROM x", dbTypeSQLServer, 100, "WITH x AS (SELECT id FROM t) SELECT * FROM x"},
		{"zero limit no-op", "SELECT id FROM t", dbTypeMySQL, 0, "SELECT id FROM t"},
		{"non select no-op", "UPDATE t SET a = 1", dbTypeMySQL, 100, "UPDATE t SET a = 1"},
	}
	for _, tc := range cases {
		if got := withRowLimit(tc.query, tc.dbType, tc.limit); got != tc.want {
			t.Errorf("%s: withRowLimit(%q, %q, %d) = %q, want %q",
				tc.name, tc.query, tc.dbType, tc.limit, got, tc.want)
		}
	}
}

// TestIsSelectQuery checks the statement dispatcher, including CTEs, which
// must run through runSelect instead of db.Exec.
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

// TestNextConnSeq guards the id-sequence seeding: counting len(saved) instead
// would recycle ids after deletions and collide with surviving connections.
// TestSuggestEntryTyping proves the wrapper never gets in the way of input:
// normal characters arrive through the promoted TypedRune, and while the
// candidate bar is open only the navigation keys are intercepted.
func TestSuggestEntryTyping(t *testing.T) {
	s := newTestEntry()
	test.Type(s, "SEL")
	if s.Entry.Text != "SEL" {
		t.Fatalf("typing through the wrapper produced %q", s.Entry.Text)
	}

	// Candidates open: plain characters still insert (they are runes, not keys).
	s.setSuggestions([]string{"SELECT", "SET"})
	test.Type(s, " *")
	if s.Entry.Text != "SEL *" {
		t.Fatalf("typing with candidates open produced %q", s.Entry.Text)
	}
	if len(s.suggest) != 2 {
		t.Fatalf("candidates lost while typing: %v", s.suggest)
	}

	// Down/Up move the selection instead of moving the caret; Enter accepts.
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	if s.sugSel != 1 {
		t.Fatalf("down key left selection at %d, want 1", s.sugSel)
	}
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyUp})
	if s.sugSel != 0 {
		t.Fatalf("up key left selection at %d, want 0", s.sugSel)
	}
	s.Entry.SetText("SELECT * FR")
	s.Entry.CursorRow, s.Entry.CursorColumn = 0, 11
	s.suggest, s.sugSel = []string{"FROM"}, 0 // what the OnChanged handler would compute
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEnter})
	if s.Entry.Text != "SELECT * FROM" {
		t.Fatalf("enter did not accept the suggestion: %q", s.Entry.Text)
	}

	// Escape dismisses without touching the text.
	s.setSuggestions([]string{"FROM"})
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if len(s.suggest) != 0 || s.Entry.Text != "SELECT * FROM" {
		t.Fatalf("escape changed state: %v %q", s.suggest, s.Entry.Text)
	}
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
