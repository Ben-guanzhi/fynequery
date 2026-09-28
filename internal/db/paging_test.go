package db

import (
	"testing"
)

// TestPageSQLDialects pins each engine's window clause. Rendering is pure SQL,
// so the whole table is checkable without a database.
func TestPageSQLDialects(t *testing.T) {
	cases := []struct {
		name   string
		dbType string
		sql    string
		page   int
		size   int
		want   string
	}{
		{"mysql page 1", DbTypeMySQL, "SELECT * FROM t", 1, 20, "SELECT * FROM t LIMIT 20 OFFSET 0"},
		{"mysql page 3", DbTypeMySQL, "SELECT * FROM t", 3, 20, "SELECT * FROM t LIMIT 20 OFFSET 40"},
		{"mysql trailing semicolon", DbTypeMySQL, "SELECT * FROM t;", 2, 50, "SELECT * FROM t LIMIT 50 OFFSET 50"},
		{"postgres with order", DbTypePostgres, "SELECT a FROM t ORDER BY a DESC", 2, 100,
			"SELECT a FROM t ORDER BY a DESC LIMIT 100 OFFSET 100"},
		{"sqlite cte", DbTypeSQLite, "WITH x AS (SELECT 1 AS a) SELECT a FROM x", 2, 10,
			"WITH x AS (SELECT 1 AS a) SELECT a FROM x LIMIT 10 OFFSET 10"},
		{"sqlserver adds order by", DbTypeSQLServer, "SELECT * FROM t", 2, 20,
			"SELECT * FROM t ORDER BY (SELECT NULL) OFFSET 20 ROWS FETCH NEXT 20 ROWS ONLY"},
		{"sqlserver keeps its own order by", DbTypeSQLServer, "SELECT * FROM t ORDER BY id", 4, 25,
			"SELECT * FROM t ORDER BY id OFFSET 75 ROWS FETCH NEXT 25 ROWS ONLY"},
		// A derived table's ORDER BY is inside parentheses, so the statement
		// still has no outer sort and needs the constant one.
		{"sqlserver inner order by is not outer", DbTypeSQLServer, "SELECT * FROM (SELECT a FROM t ORDER BY a) x", 2, 10,
			"SELECT * FROM (SELECT a FROM t ORDER BY a) x ORDER BY (SELECT NULL) OFFSET 10 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"oracle", DbTypeOracle, "SELECT * FROM t", 5, 30,
			"SELECT * FROM t OFFSET 120 ROWS FETCH NEXT 30 ROWS ONLY"},
	}
	for _, tc := range cases {
		got, err := PageSQL(tc.dbType, tc.sql, tc.page, tc.size)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestPageSQLRefusals checks the guards that keep paging from rewriting SQL the
// user constrained themselves, and from inventing a window out of range.
func TestPageSQLRefusals(t *testing.T) {
	bad := []struct {
		name   string
		dbType string
		sql    string
		page   int
		size   int
	}{
		{"own limit", DbTypeMySQL, "SELECT * FROM t LIMIT 5", 1, 20},
		{"own offset", DbTypePostgres, "SELECT * FROM t OFFSET 10", 1, 20},
		{"sqlserver top", DbTypeSQLServer, "SELECT TOP (10) * FROM t", 1, 20},
		{"oracle rownum", DbTypeOracle, "SELECT * FROM t WHERE ROWNUM < 10", 1, 20},
		{"update", DbTypeMySQL, "UPDATE t SET a = 1", 1, 20},
		{"insert", DbTypeMySQL, "INSERT INTO t VALUES (1)", 1, 20},
		{"catalog show", DbTypeMySQL, "SHOW TABLES", 1, 20},
		{"explain", DbTypeMySQL, "EXPLAIN SELECT * FROM t", 1, 20},
		{"unknown engine", "nosuchengine", "SELECT 1", 1, 20},
		{"page zero", DbTypeMySQL, "SELECT * FROM t", 0, 20},
		{"size zero", DbTypeMySQL, "SELECT * FROM t", 1, 0},
		{"size too big", DbTypeMySQL, "SELECT * FROM t", 1, MaxPageSize + 1},
	}
	for _, tc := range bad {
		if _, err := PageSQL(tc.dbType, tc.sql, tc.page, tc.size); err == nil {
			t.Errorf("%s: accepted (%s)", tc.name, tc.sql)
		}
	}

	// The predicate agrees with the renderer, including the positive cases.
	for _, ok := range []struct {
		dbType, sql string
	}{
		{DbTypeMySQL, "SELECT * FROM t"},
		{DbTypeMySQL, "  select a, b from t where a > 1 "},
		{DbTypePostgres, "WITH x AS (SELECT 1 AS a) SELECT * FROM x"},
	} {
		if !Pageable(ok.dbType, ok.sql) {
			t.Errorf("Pageable(%s, %q) = false, want true", ok.dbType, ok.sql)
		}
	}
	if Pageable(DbTypeMySQL, "SELECT * FROM t LIMIT 3") {
		t.Error("a statement with its own LIMIT must not look pageable")
	}
}

// TestPageableIsDeliberatelyCoarse documents the tradeoff: the guard reads the
// window words anywhere in the statement, so a column named limit_count
// disables paging. A false refusal costs a disabled button; a false acceptance
// would rewrite SQL the user already constrained.
func TestPageableIsDeliberatelyCoarse(t *testing.T) {
	if Pageable(DbTypeMySQL, "SELECT * FROM t WHERE name = 'limit 3'") {
		t.Error("the word limit anywhere in the statement must disable paging")
	}
}

// TestPageNeedsStableOrder checks who decides a page boundary is trustworthy:
// only an ORDER BY that belongs to the statement itself counts.
func TestPageNeedsStableOrder(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"SELECT * FROM t", true},
		{"SELECT * FROM t;", true},
		{"SELECT a FROM t ORDER BY a DESC", false},
		{"SELECT a FROM t WHERE a > 1 ORDER BY a, b DESC", false},
		{"WITH x AS (SELECT 1 AS a) SELECT * FROM x", true},
		// The inner sort orders the derived table, not the sliced rows.
		{"SELECT * FROM (SELECT a FROM t ORDER BY a) x", true},
		// A trailing comment must not hide the sort.
		{"SELECT a FROM t ORDER BY a -- note", false},
	}
	for _, tc := range cases {
		if got := PageNeedsStableOrder(tc.sql); got != tc.want {
			t.Errorf("PageNeedsStableOrder(%q) = %v, want %v", tc.sql, got, tc.want)
		}
	}
}

// TestWithOrderBy pins the rewrite that makes the windows disjoint, including
// per-engine quoting and the composite key order.
func TestWithOrderBy(t *testing.T) {
	cases := []struct {
		dbType string
		sql    string
		cols   []string
		want   string
	}{
		{DbTypeMySQL, "SELECT * FROM t", []string{"id"}, "SELECT * FROM t ORDER BY `id`"},
		{DbTypeMySQL, "SELECT * FROM t;", []string{"id"}, "SELECT * FROM t ORDER BY `id`"},
		{DbTypePostgres, "SELECT * FROM t", []string{"a", "b"}, `SELECT * FROM t ORDER BY "a", "b"`},
		{DbTypeSQLServer, "SELECT * FROM t", []string{"id"}, "SELECT * FROM t ORDER BY [id]"},
		{DbTypeOracle, "SELECT * FROM T", []string{"ID"}, `SELECT * FROM T ORDER BY "ID"`},
	}
	for _, tc := range cases {
		got, err := WithOrderBy(tc.dbType, tc.sql, tc.cols)
		if err != nil {
			t.Errorf("%s %q: %v", tc.dbType, tc.sql, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.dbType, got, tc.want)
		}
	}
	if _, err := WithOrderBy(DbTypeMySQL, "SELECT * FROM t", nil); err == nil {
		t.Error("an empty ordering list must be refused")
	}

	// The rewritten statement stays pageable and now counts as ordered, so the
	// pager neither rewrites it twice nor adds a constant sort on top.
	ordered, err := WithOrderBy(DbTypeMySQL, "SELECT * FROM t", []string{"id"})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !Pageable(DbTypeMySQL, ordered) {
		t.Error("the rewritten statement must still be pageable")
	}
	if PageNeedsStableOrder(ordered) {
		t.Error("the rewritten statement must count as ordered")
	}
	if got, err := PageSQL(DbTypeMySQL, ordered, 2, 20); err != nil ||
		got != "SELECT * FROM t ORDER BY `id` LIMIT 20 OFFSET 20" {
		t.Errorf("PageSQL of the rewrite = %q (%v)", got, err)
	}
}
