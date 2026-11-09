package main

import (
	"strings"
	"testing"

	"github.com/dbcore/dbcore/sqltext"

	"fynequery/db"
)

// TestQueryPaneUsesLiveSession guards against the pane holding the session it
// was built with. The workspace closes that session when the user switches
// databases, so Run must consult the live session instead (otherwise the
// plugin answers "unknown connection ... (closed?)").
func TestQueryPaneUsesLiveSession(t *testing.T) {
	stale := &fakeSession{}
	live := &fakeSession{}

	ws := &workspace{sess: stale}
	qp := &queryPane{sess: stale, sessFn: func() db.Session { return ws.sess }}

	if got := qp.session(); got != db.Session(stale) {
		t.Fatalf("before switch: got %v want stale", got)
	}
	ws.sess = live // switchDatabase closes stale and installs a new session
	if got := qp.session(); got != db.Session(live) {
		t.Fatalf("after switch: got %v want live", got)
	}
}

func TestQueryPaneSessionFallsBackToField(t *testing.T) {
	sess := &fakeSession{}
	qp := &queryPane{sess: sess}
	if got := qp.session(); got != db.Session(sess) {
		t.Fatalf("got %v want sess", got)
	}
}

// TestShowQueryRowFillsDetails checks that selecting a row in a SQL result
// grid populates the right 详情 pane, read-only (query results have no key to
// write back through).
func TestShowQueryRowFillsDetails(t *testing.T) {
	ws := &workspace{details: newDetailsPane()}
	data := &TableData{
		Cols: []ColumnInfo{{Name: "id", Type: "bigint"}, {Name: "name", Type: "varchar"}},
		Rows: [][]*string{{str("1"), str("pump")}},
	}

	ws.showQueryRow(data, 0)
	if len(ws.details.fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(ws.details.fields))
	}
	if got := ws.details.fields[0]; got.Name != "id" || !got.ReadOnly {
		t.Fatalf("field[0] = %+v, want id read-only", got)
	}
	if v := ws.details.fields[1].Value; v == nil || *v != "pump" {
		t.Fatalf("field[1] value = %v, want pump", v)
	}

	// Out-of-range clears back to the prompt.
	ws.showQueryRow(data, 9)
	if len(ws.details.fields) != 0 {
		t.Fatalf("out-of-range should clear details, got %d fields", len(ws.details.fields))
	}
}

// TestQuoteTableMatchesSqltext pins the table quoter to the shared dialect
// layer. It used to hand-roll MySQL backticks and ANSI for everything else,
// which silently missed SQL Server's brackets.
func TestQuoteTableMatchesSqltext(t *testing.T) {
	drivers := []string{"mysql", "mariadb", "postgres", "postgresql", "sqlserver", "mssql", "oracle", "sqlite", "nope"}
	names := []string{"t", "a`b", `a"b`, "a]b", strings.Repeat("x", 300), "a\x00b"}
	for _, driver := range drivers {
		for _, name := range names {
			ws := &workspace{conn: Connection{Driver: driver}}
			if got, want := ws.quoteTable(name), sqltext.QuoteIdentLoose(driver, name); got != want {
				t.Errorf("quoteTable(%q, %q) = %q, want %q", name, driver, got, want)
			}
		}
	}
}

// TestTruncationNote checks the status-line decoration a capped result gets.
// The flag used to be dropped on the floor: the first page was shown as if it
// were the whole result, which is exactly what sqlkit.ReadRows now reports and
// what this note is for.
func TestTruncationNote(t *testing.T) {
	if got := truncationNote("42 rows", false); got != "42 rows" {
		t.Errorf("truncationNote(false) = %q, want unchanged", got)
	}
	if got := truncationNote("42 rows", true); got != "42 rows · truncated" {
		t.Errorf("truncationNote(true) = %q, want a truncation note", got)
	}
}
