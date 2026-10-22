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
	return proto.QueryResult{Columns: []proto.ColumnInfo{{Name: "?1"}}, Rows: [][]*string{}}, nil
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

func main() {
	banner := proto.Banner{Proto: proto.ProtocolName, Version: proto.ProtocolVersion, Code: "fake", Name: "FakeDB"}
	if err := proto.Serve(fakeHandler{}, banner); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
