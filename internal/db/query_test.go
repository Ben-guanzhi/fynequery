package db

import (
	"context"
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

// TestJudgeRowCap pins the pre-run verdict against the same shapes as
// TestWithRowLimit: the UI only has the statement, so the verdict must agree
// with what the renderer will actually do — including the conservative refusals
// where a word inside the SQL (a column named offset) reads as a row window.
func TestJudgeRowCap(t *testing.T) {
	cases := []struct {
		name   string
		dbType string
		query  string
		limit  int
		want   RowCapStatus
	}{
		{"plain select binds", DbTypeMySQL, "SELECT id FROM t", 100, RowCapApplied},
		{"cte binds on pg", DbTypePostgres, "WITH x AS (SELECT id FROM t) SELECT * FROM x", 100, RowCapApplied},
		{"no cap asked", DbTypeMySQL, "SELECT id FROM t", 0, RowCapNotRequested},
		{"own limit", DbTypeSQLite, "SELECT id FROM t LIMIT 5", 100, RowCapOwnWindow},
		{"own top", DbTypeSQLServer, "SELECT TOP 5 id FROM t", 100, RowCapOwnWindow},
		{"own offset clause", DbTypeOracle, "SELECT id FROM t OFFSET 10 ROWS FETCH NEXT 5 ROWS ONLY", 100, RowCapOwnWindow},
		// The documented conservative direction: the word, not the clause.
		{"column named offset", DbTypeMySQL, "SELECT offset FROM t", 100, RowCapOwnWindow},
		{"mssql cte has nowhere to inject", DbTypeSQLServer, "WITH x AS (SELECT id FROM t) SELECT * FROM x", 100, RowCapShapeUnsafe},
	}
	for _, tc := range cases {
		if got := JudgeRowCap(tc.dbType, tc.query, tc.limit); got != tc.want {
			t.Errorf("%s: JudgeRowCap(%q, %q, %d) = %d, want %d",
				tc.name, tc.dbType, tc.query, tc.limit, got, tc.want)
		}
	}
}

// TestExecuteRowCapProbe is the truncation split measured against a real
// database: five rows under a cap of five and three rows under the same cap both
// report the row count the grid shows, and only the one extra row the probe asks
// for says which of the two the user is looking at. The two cases the probe must
// not touch are a statement that windows its own rows and 无限制 - the first is
// run as written, the second asked for no cap at all.
func TestExecuteRowCapProbe(t *testing.T) {
	store, file := newChangeStore(t, itemsSeed, itemsRows,
		`INSERT INTO items (id, name, note) VALUES (4, 'delta', 'n4'), (5, 'echo', 'n5')`)
	conn, err := store.OpenConn(changeConnID)
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	read := func(query string, limit int) QueryResult {
		t.Helper()
		res := store.Execute(context.Background(), conn, file, query, limit)
		if res.Error != "" {
			t.Fatalf("%q (cap %d): %s", query, limit, res.Error)
		}
		return res
	}
	const ordered = "SELECT id FROM items ORDER BY id"

	if whole := read(ordered, 5); whole.RowCount != 5 || whole.Truncated {
		t.Errorf("five rows under a cap of five = %d rows, truncated=%v; the whole answer may not claim a cut",
			whole.RowCount, whole.Truncated)
	}
	cut := read(ordered, 3)
	if cut.RowCount != 3 || len(cut.Rows) != 3 || !cut.Truncated {
		t.Errorf("five rows under a cap of three = RowCount %d, %d rows kept, truncated=%v; want 3, 3, true",
			cut.RowCount, len(cut.Rows), cut.Truncated)
	}
	// The probe row must not survive in the data the grid renders: the slice
	// stays the first three, so the fourth id may not appear anywhere in it.
	for i, row := range cut.Rows {
		if got := ValueString(row[0]); got == "4" || got == "5" {
			t.Errorf("row %d = %q, want the kept slice to be the first three ids", i, got)
		}
	}
	if own := read(ordered+" LIMIT 2", 3); own.RowCount != 2 || own.Truncated {
		t.Errorf("own window = %d rows, truncated=%v; a statement that caps itself is run as written",
			own.RowCount, own.Truncated)
	}
	// The shape the guard exists for: the statement's own window is wider than
	// the cap, so the rows on screen do exceed it. Trimming them here would be
	// the client editing an answer the server was told to shape, and the flag
	// would blame a cap that was never injected.
	if wider := read(ordered+" LIMIT 5", 3); wider.RowCount != 5 || wider.Truncated {
		t.Errorf("own window above the cap = %d rows, truncated=%v; want 5 rows, false",
			wider.RowCount, wider.Truncated)
	}
	if free := read(ordered, 0); free.RowCount != 5 || free.Truncated {
		t.Errorf("no cap = %d rows, truncated=%v; 无限制 asked for no cut to report", free.RowCount, free.Truncated)
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
