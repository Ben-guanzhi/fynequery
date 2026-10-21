package main

// Unit tests for the batch-2 cluster: statement splitting / ranges,
// timeout detection, saved queries + keyword bindings.

import (
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// newEditorForTest builds a headless editor with given text and cursor.
func newEditorForTest(text string, row, col int) *widget.Entry {
	e := widget.NewMultiLineEntry()
	e.SetText(text)
	e.CursorRow = row
	e.CursorColumn = col
	return e
}

// --- statement splitting ----------------------------------------------------

func TestSplitStatementsForRunQuotesAndComments(t *testing.T) {
	script := "SELECT ';' FROM t; -- trailing ; comment\nSELECT a FROM t WHERE b = 'x;y'; /* c; d */ SELECT 2"
	got := splitStatementsForRun(script)
	want := []string{
		"SELECT ';' FROM t",
		"-- trailing ; comment\nSELECT a FROM t WHERE b = 'x;y'",
		"/* c; d */ SELECT 2",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d statements, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stmt[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitStatementsForRunBlankDropped(t *testing.T) {
	if got := splitStatementsForRun("SELECT 1;; ;  SELECT 2"); len(got) != 2 {
		t.Fatalf("blank statements should be dropped, got %d: %q", len(got), got)
	}
}

func TestTopLevelStatementRanges(t *testing.T) {
	script := "SELECT 1; SELECT 2"
	ranges := topLevelStatementRanges(script)
	// One ';' yields 2 spans: [0,9) = "SELECT 1;" and [9,17) = " SELECT 2".
	if len(ranges) != 2 {
		t.Fatalf("ranges = %d, want 2: %+v", len(ranges), ranges)
	}
	if ranges[0].start != 0 || ranges[0].end != 9 {
		t.Errorf("range0 = {%d,%d}, want {0,9}", ranges[0].start, ranges[0].end)
	}
	if s := strings.TrimSpace(script[ranges[1].start:ranges[1].end]); s != "SELECT 2" {
		t.Errorf("range1 text = %q, want 'SELECT 2'", s)
	}
}

// --- timeout detection ---------------------------------------------------------

func TestLooksLikeTimeout(t *testing.T) {
	for _, err := range []error{
		dbErr("i/o timeout"),
		dbErr("context deadline exceeded"),
		dbErr("read tcp 1.2.3.4:5432: connection timed out"),
	} {
		if !looksLikeTimeout(err) {
			t.Errorf("expected timeout for %q", err)
		}
	}
	if looksLikeTimeout(dbErr("unknown column 'x'")) {
		t.Error("plain SQL error must not look like a timeout")
	}
	if looksLikeTimeout(db.ErrQueryCancelled) {
		t.Error("cancellation must not be treated as a timeout")
	}
}

func dbErr(s string) error { return errorString(s) }

type errorString string

func (e errorString) Error() string { return string(e) }

// --- saved queries -------------------------------------------------------------

func withTempQueries(t *testing.T) {
	t.Helper()
	orig := savedQueriesPath
	savedQueriesPath = filepath.Join(t.TempDir(), "queries.json")
	t.Cleanup(func() { savedQueriesPath = orig })
}

func TestSavedQueriesRoundTrip(t *testing.T) {
	withTempQueries(t)
	qs := []SavedQuery{
		{Name: "orders", Keywords: []string{"ord", "o1"}, SQL: "SELECT * FROM orders"},
		{Name: "users", Keywords: nil, SQL: "SELECT 1"},
	}
	if err := saveSavedQueries(qs); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := loadSavedQueries()
	if len(got) != 2 || got[0].Name != "orders" || got[0].SQL != "SELECT * FROM orders" {
		t.Fatalf("round-trip = %+v", got)
	}
	// Delete the first one, confirm persistence.
	if err := deleteSavedQuery("orders"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got = loadSavedQueries()
	if len(got) != 1 || got[0].Name != "users" {
		t.Fatalf("after delete = %+v", got)
	}
}

func TestSavedQueryKeywords(t *testing.T) {
	withTempQueries(t)
	_ = saveSavedQueries([]SavedQuery{
		{Name: "big", Keywords: []string{"showbig"}, SQL: "SELECT * FROM big"},
	})
	// Exact keyword (any case) yields its query; a prefix does not.
	kws := savedQueryKeywords()
	if qs, ok := kws["showbig"]; !ok || len(qs) != 1 || qs[0].Name != "big" {
		t.Fatalf("keywords[showbig] = %+v", kws["showbig"])
	}
	// sortedSavedQueries returns stable order.
	sorted := sortedSavedQueries()
	if len(sorted) != 1 || sorted[0].Name != "big" {
		t.Fatalf("sorted = %+v", sorted)
	}
}

// --- keyword completion ---------------------------------------------------------

// queryKeywordSuggestions must only offer queries whose keyword prefix-matches
// the current word (case-insensitively).
func TestQueryKeywordSuggestionsPrefix(t *testing.T) {
	withTempQueries(t)
	_ = saveSavedQueries([]SavedQuery{
		{Name: "a", Keywords: []string{"alpha"}, SQL: "SELECT 1"},
		{Name: "b", Keywords: []string{"beta"}, SQL: "SELECT 2"},
	})
	qp := &queryPane{}
	got := qp.queryKeywordSuggestions("al")
	if len(got) != 1 || !strings.Contains(got[0].label, "alpha") {
		t.Fatalf("prefix 'al' should match only alpha, got %+v", got)
	}
	if got := qp.queryKeywordSuggestions("zeta"); len(got) != 0 {
		t.Fatalf("no keyword starts with 'zeta', got %+v", got)
	}
	// Empty word lists all keywords.
	if got := qp.queryKeywordSuggestions(""); len(got) != 2 {
		t.Fatalf("empty word should list 2 keywords, got %+v", got)
	}
}

// currentStatementRange must resolve the statement containing the cursor.
func TestCurrentStatementRange(t *testing.T) {
	qp := &queryPane{editor: newEditorForTest("SELECT 1;\nSELECT 2", 0, 0)}
	// Cursor at the very start sits in statement 1.
	s, e := qp.currentStatementRange()
	if strings.TrimSpace(qp.editor.Text[s:e]) != "SELECT 1;" {
		t.Errorf("range at cursor(0,0) = %q", qp.editor.Text[s:e])
	}
	// Cursor inside the second statement.
	qp.editor.CursorRow = 1
	qp.editor.CursorColumn = 6
	s, e = qp.currentStatementRange()
	if strings.TrimSpace(qp.editor.Text[s:e]) != "SELECT 2" {
		t.Errorf("range on line 2 = %q", qp.editor.Text[s:e])
	}
}
