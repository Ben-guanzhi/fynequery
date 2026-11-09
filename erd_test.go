package main

// Tests for ER-diagram foreign-key discovery across engines. The pure helpers
// (query selection, PRAGMA argument quoting) are checked directly, and the
// SQLite per-table path is exercised through a fake session.

import (
	"reflect"
	"testing"

	"fyne.io/fyne/v2"

	"fynequery/proto"
)

func TestERFKQueryCoversEngines(t *testing.T) {
	withQuery := []string{"mysql", "mariadb", "postgres", "postgresql", "mssql", "sqlserver", "oracle"}
	for _, d := range withQuery {
		if erFKQuery(d) == "" {
			t.Errorf("driver %q should have an FK query", d)
		}
	}
	// SQLite discovers FKs per-table via PRAGMA (handled in fetchERForeignKeys),
	// so it intentionally has no single catalog query; unknown drivers likewise.
	for _, d := range []string{"sqlite", "unknown", ""} {
		if erFKQuery(d) != "" {
			t.Errorf("driver %q should not have a single FK query, got %q", d, erFKQuery(d))
		}
	}
}

func TestSQLitePragmaArg(t *testing.T) {
	cases := map[string]string{
		"orders":     "'orders'",
		"o'brien":    "'o''brien'",
		`weird"name`: `'weird"name'`,
	}
	for in, want := range cases {
		if got := sqlitePragmaArg(in); got != want {
			t.Errorf("sqlitePragmaArg(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeERDSession answers Query with canned results keyed by the SQL text.
type fakeERDSession struct {
	queries map[string]proto.QueryResult
}

func (f fakeERDSession) Server() string                      { return "" }
func (f fakeERDSession) Tables() ([]proto.ObjectInfo, error) { return nil, nil }
func (f fakeERDSession) Columns(string) ([]proto.ColumnInfo, error) {
	return nil, nil
}
func (f fakeERDSession) Browse(string, int) (proto.QueryResult, error) {
	return proto.QueryResult{}, nil
}
func (f fakeERDSession) Close() {}
func (f fakeERDSession) Query(sql string, _ int) (proto.QueryResult, error) {
	if res, ok := f.queries[sql]; ok {
		return res, nil
	}
	return proto.QueryResult{}, nil
}

// ct is a text cell for the tests.
func ct(v string) proto.CellPayload { return proto.CellPayload{Kind: "text", Text: v} }

func TestFetchERForeignKeysSQLite(t *testing.T) {
	// PRAGMA foreign_key_list columns: id, seq, table, from, to, on_update, on_delete, match.
	pragmaSQL := "PRAGMA foreign_key_list('orders')"
	sess := fakeERDSession{queries: map[string]proto.QueryResult{
		pragmaSQL: {Rows: [][]proto.CellPayload{
			{ct("0"), ct("0"), ct("customers"), ct("customer_id"), ct("id"), ct("NO ACTION"), ct("NO ACTION"), ct("NONE")},
		}},
	}}
	fks := fetchERForeignKeys(sess, "sqlite", []string{"orders", "products"})
	want := []erFK{{From: "orders", FromCol: "customer_id", To: "customers", ToCol: "id"}}
	if !reflect.DeepEqual(fks, want) {
		t.Fatalf("sqlite FKs = %+v, want %+v", fks, want)
	}
}

// ---- layered + barycenter layout (ported from TableProGo erLayout.ts) ----

// erPosOf runs layoutERModel and returns name → position.
func erPosOf(m *erModel) map[string]fyne.Position {
	layoutERModel(m)
	out := make(map[string]fyne.Position, len(m.Tables))
	for i := range m.Tables {
		out[m.Tables[i].Name] = m.Tables[i].Pos
	}
	return out
}

// TestLayoutERModelLayersLeftToRight: referenced tables sit left of their
// references (level = longest FK path).
func TestLayoutERModelLayersLeftToRight(t *testing.T) {
	m := &erModel{
		Tables: []erTable{{Name: "customers"}, {Name: "orders"}, {Name: "order_items"}},
		FKs: []erFK{
			{From: "orders", FromCol: "customer_id", To: "customers", ToCol: "id"},
			{From: "order_items", FromCol: "order_id", To: "orders", ToCol: "id"},
		},
	}
	pos := erPosOf(m)
	if pos["customers"].X >= pos["orders"].X {
		t.Fatalf("customers (%v) must be left of orders (%v)", pos["customers"], pos["orders"])
	}
	if pos["orders"].X >= pos["order_items"].X {
		t.Fatalf("orders (%v) must be left of order_items (%v)", pos["orders"], pos["order_items"])
	}
}

// TestLayoutERModelBarycenterFixesCrossings: alphabetical order puts C above
// D in column 1 while C→B and D→A cross; the barycenter sweeps must pull D
// level with A and C level with B (D above C afterwards).
func TestLayoutERModelBarycenterFixesCrossings(t *testing.T) {
	m := &erModel{
		Tables: []erTable{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}},
		FKs: []erFK{
			{From: "c", FromCol: "b_id", To: "b", ToCol: "id"},
			{From: "d", FromCol: "a_id", To: "a", ToCol: "id"},
		},
	}
	pos := erPosOf(m)
	// Same column (both level 1): x equal.
	if pos["c"].X != pos["d"].X {
		t.Fatalf("c and d must share a column: c.X=%v d.X=%v", pos["c"].X, pos["d"].X)
	}
	if pos["d"].Y >= pos["c"].Y {
		t.Fatalf("barycenter must pull d (refs a, row 0) above c (refs b, row 1): d.Y=%v c.Y=%v", pos["d"].Y, pos["c"].Y)
	}
	// a and b share column 0.
	if pos["a"].X != pos["b"].X {
		t.Fatalf("a and b must share a column: a.X=%v b.X=%v", pos["a"].X, pos["b"].X)
	}
	if pos["a"].Y >= pos["b"].Y {
		t.Fatalf("alphabetical seed must keep a above b: a.Y=%v b.Y=%v", pos["a"].Y, pos["b"].Y)
	}
}

// TestLayoutERModelCycleSafe: mutual FKs resolve via the depth guard instead
// of overflowing the stack.
func TestLayoutERModelCycleSafe(t *testing.T) {
	m := &erModel{
		Tables: []erTable{{Name: "a"}, {Name: "b"}},
		FKs: []erFK{
			{From: "a", FromCol: "b_id", To: "b", ToCol: "id"},
			{From: "b", FromCol: "a_id", To: "a", ToCol: "id"},
		},
	}
	pos := erPosOf(m)
	if pos["a"].X == pos["b"].X && pos["a"].Y == pos["b"].Y {
		t.Fatalf("cycle tables must get distinct positions: %v %v", pos["a"], pos["b"])
	}
}

// TestLayoutERModelDeterministic: the same schema in different input orders
// yields byte-identical positions (K-F3 determinism contract).
func TestLayoutERModelDeterministic(t *testing.T) {
	fks := []erFK{
		{From: "orders", FromCol: "customer_id", To: "customers", ToCol: "id"},
		{From: "order_items", FromCol: "order_id", To: "orders", ToCol: "id"},
		{From: "reviews", FromCol: "order_id", To: "orders", ToCol: "id"},
	}
	names := []string{"customers", "orders", "order_items", "reviews"}
	m1 := &erModel{Tables: []erTable{{Name: names[0]}, {Name: names[1]}, {Name: names[2]}, {Name: names[3]}}, FKs: append([]erFK(nil), fks...)}
	m2 := &erModel{Tables: []erTable{{Name: names[3]}, {Name: names[1]}, {Name: names[0]}, {Name: names[2]}}, FKs: append([]erFK(nil), fks...)}
	p1, p2 := erPosOf(m1), erPosOf(m2)
	for _, n := range names {
		if p1[n] != p2[n] {
			t.Fatalf("position of %s differs by input order: %v vs %v", n, p1[n], p2[n])
		}
	}
}
