// fakeplugin is a plugin executable used only by the db package tests: it
// speaks the plugin protocol against in-memory state, touching no database.
package main

import (
	"errors"
	"fmt"
	"os"

	"fynequery/proto"
)

type fakeHandler struct{}

func (fakeHandler) Meta() proto.MetaResult {
	return proto.MetaResult{Code: "fake", Name: "FakeDB", Version: "test", DefaultPort: "1234"}
}

func (fakeHandler) Open(p proto.ConnectParams) (proto.OpenResult, error) {
	if p.Host == "boom" {
		return proto.OpenResult{}, errors.New("simulated dial failure")
	}
	return proto.OpenResult{ConnID: "c-" + p.Name, Server: "FakeDB 1.2.3"}, nil
}

func (fakeHandler) Close(p proto.CloseParams) error { return nil }

func (fakeHandler) Tables(p proto.TablesParams) (proto.TablesResult, error) {
	return proto.TablesResult{Objects: []proto.ObjectInfo{
		{Name: "alpha", Kind: "table", Rows: 42, ApproxRows: true},
		{Name: "beta_view", Kind: "view", Rows: -1},
	}}, nil
}

func (fakeHandler) Columns(p proto.ColumnsParams) (proto.ColumnsResult, error) {
	return proto.ColumnsResult{Columns: []proto.ColumnInfo{
		{Name: "id", Type: "int4", Key: "PK"},
		{Name: "name", Type: "varchar", Nullable: true},
	}}, nil
}

func (fakeHandler) Query(p proto.QueryParams) (proto.QueryResult, error) {
	// Row 2 is a SQL NULL and row 3 an empty string. On the old all-string
	// wire the two were the same value; the tagged form keeps them apart, and
	// TestPluginEndToEnd asserts they arrive that way through the real pipe.
	return proto.QueryResult{
		Columns: []proto.ColumnInfo{{Name: "?1"}},
		Rows:    [][]proto.CellPayload{{proto.TextCell("1")}, {proto.NullCell()}, {proto.TextCell("")}},
	}, nil
}

func (fakeHandler) Browse(p proto.BrowseParams) (proto.QueryResult, error) {
	return fakeHandler{}.Query(proto.QueryParams{})
}

func (fakeHandler) CreateDatabase(p proto.CreateDBParams) error {
	if p.Name == "boom" {
		return errors.New("simulated create database failure")
	}
	return nil
}

// The DML methods back the editable-grid path in the db package tests. The fake
// plugin does not touch a real database; it just records that the call arrived
// and returns a successful single-row affected count.
func (fakeHandler) Update(p proto.DMLSingleParams) (proto.DMLResult, error) {
	return proto.DMLResult{RowsAffected: 1}, nil
}

func (fakeHandler) Insert(p proto.DMLSingleParams) (proto.DMLResult, error) {
	return proto.DMLResult{RowsAffected: 1}, nil
}

func (fakeHandler) Delete(p proto.DMLSingleParams) (proto.DMLResult, error) {
	return proto.DMLResult{RowsAffected: 1}, nil
}

func (fakeHandler) ApplyBatch(p proto.DMLBatchParams) (proto.DMLResult, error) {
	n := len(p.Updates) + len(p.Deletes) + len(p.Inserts)
	return proto.DMLResult{RowsAffected: int64(n)}, nil
}

func (fakeHandler) Begin(p proto.TxParams) error    { return nil }
func (fakeHandler) Commit(p proto.TxParams) error   { return nil }
func (fakeHandler) Rollback(p proto.TxParams) error { return nil }

// QueryStream emits a few rows in two frames to exercise the streaming path.
func (fakeHandler) QueryStream(p proto.QueryStreamParams, emit func(proto.StreamFrame) error) (proto.QueryResult, error) {
	if err := emit(proto.StreamFrame{
		Columns: []proto.ColumnInfo{{Name: "n"}},
		Rows:    [][]proto.CellPayload{{ptrS("1")}, {ptrS("2")}},
	}); err != nil {
		return proto.QueryResult{}, err
	}
	if err := emit(proto.StreamFrame{Rows: [][]proto.CellPayload{{ptrS("3")}}}); err != nil {
		return proto.QueryResult{}, err
	}
	return proto.QueryResult{RowsAffected: 0, DurationMS: 1}, nil
}

// ptrS is a text cell for the fake plugin.
func ptrS(s string) proto.CellPayload { return proto.CellPayload{Kind: "text", Text: s} }

// Metrics returns a small fixed set so the host metrics path can be tested.
func (fakeHandler) Metrics(p proto.MetricsParams) (proto.MetricsResult, error) {
	return proto.MetricsResult{Metrics: []proto.Metric{
		{Label: "Connections", Value: "7", Group: "Server"},
		{Label: "Uptime", Value: "3600s", Group: "Server"},
	}}, nil
}

func main() {
	banner := proto.Banner{Proto: proto.ProtocolName, Version: proto.ProtocolVersion, Code: "fake", Name: "FakeDB"}
	if err := proto.Serve(fakeHandler{}, banner); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
