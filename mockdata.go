package main

// Grid data model. The original mock dataset is gone; these types now shape
// real query results coming from the db layer (built-in MySQL or plugins).

import (
	"fynequery/proto"
)

// ColumnInfo describes one grid column.
type ColumnInfo struct {
	Name     string
	Type     string
	Nullable bool
	Key      string // "PK" for primary key columns
}

// TableInfo is a sidebar tree entry.
type TableInfo struct {
	Name   string
	Kind   string // "table" | "view"
	Rows   int64
	Approx bool
}

// TableData is what the grid renders.
type TableData struct {
	Cols    []ColumnInfo
	Rows    [][]*string // nil cell = NULL
	Total   int         // total rows (approximate when Approx)
	Fetched int         // rows actually fetched
	Approx  bool        // total is an estimate (SHOW ~N in the status bar)
}

// str returns a pointer to s (helper for building rows).
func str(s string) *string { return &s }

// tableDataFromResult shapes a driver QueryResult for the grid.
// totalRows/-1: unknown total; approx marks it as an estimate.
func tableDataFromResult(res proto.QueryResult, totalRows int, approx bool) *TableData {
	td := &TableData{
		Rows:    res.Rows,
		Fetched: len(res.Rows),
		Total:   totalRows,
		Approx:  approx,
	}
	if td.Total < td.Fetched {
		td.Total = td.Fetched
	}
	for _, c := range res.Columns {
		td.Cols = append(td.Cols, ColumnInfo{
			Name:     c.Name,
			Type:     c.Type,
			Nullable: c.Nullable,
			Key:      c.Key,
		})
	}
	return td
}

// emptyTableData is the placeholder shown before anything is loaded.
func emptyTableData() *TableData {
	return &TableData{}
}
