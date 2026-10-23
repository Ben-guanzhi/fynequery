package main

// Tests for ER-diagram foreign-key discovery across engines. The pure helpers
// (query selection, PRAGMA argument quoting) are checked directly, and the
// SQLite per-table path is exercised through a fake session.

import (
	"reflect"
	"testing"

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

func TestFetchERForeignKeysSQLite(t *testing.T) {
	// PRAGMA foreign_key_list columns: id, seq, table, from, to, on_update, on_delete, match.
	pragmaSQL := "PRAGMA foreign_key_list('orders')"
	sess := fakeERDSession{queries: map[string]proto.QueryResult{
		pragmaSQL: {Rows: [][]*string{
			{strp("0"), strp("0"), strp("customers"), strp("customer_id"), strp("id"), strp("NO ACTION"), strp("NO ACTION"), strp("NONE")},
		}},
	}}
	fks := fetchERForeignKeys(sess, "sqlite", []string{"orders", "products"})
	want := []erFK{{From: "orders", FromCol: "customer_id", To: "customers", ToCol: "id"}}
	if !reflect.DeepEqual(fks, want) {
		t.Fatalf("sqlite FKs = %+v, want %+v", fks, want)
	}
}
