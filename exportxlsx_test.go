package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// TestBuildWorkbookRoundTrip renders a small workbook and reads it back with
// excelize itself: typed cells must survive as real numbers and dates, not
// strings, and the header must be the first row.
func TestBuildWorkbookRoundTrip(t *testing.T) {
	columns := []WorkbookColumn{
		{Name: "id", Type: "int"},
		{Name: "name", Type: "varchar(50)"},
		{Name: "created", Type: "datetime"},
		{Name: "score", Type: "decimal(10,2)"},
	}
	rows := [][]interface{}{
		{int64(7), "plain", "2024-01-02T15:04:05Z", "1.50"},
		{int64(8), "=1+1", "not a date", ""},
	}
	data, err := buildWorkbook(columns, rows)
	if err != nil {
		t.Fatalf("buildWorkbook: %v", err)
	}

	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open workbook: %v", err)
	}
	defer f.Close()

	sheet := workbookSheetName
	if v, _ := f.GetCellValue(sheet, "A1"); v != "id" {
		t.Errorf("header A1 = %q, want id", v)
	}
	if v, _ := f.GetCellValue(sheet, "A2"); v != "7" {
		t.Errorf("A2 = %q, want 7", v)
	}
	// "=1+1" must be text, not a formula Excel would evaluate.
	if v, _ := f.GetCellValue(sheet, "B3"); v != "=1+1" {
		t.Errorf("B3 = %q, want literal =1+1", v)
	}
	// A date-typed column stores a real time value, formatted by the style.
	if v, _ := f.GetCellValue(sheet, "C2"); v != "2024-01-02 15:04:05" {
		t.Errorf("C2 = %q, want 2024-01-02 15:04:05", v)
	}
	// Text that cannot parse as a date stays text.
	if v, _ := f.GetCellValue(sheet, "C3"); v != "not a date" {
		t.Errorf("C3 = %q, want the original text", v)
	}
	// An empty string in a numeric column is an empty cell, and the row still
	// lines up.
	if v, _ := f.GetCellValue(sheet, "D3"); v != "" {
		t.Errorf("D3 = %q, want empty", v)
	}
	if v, _ := f.GetCellValue(sheet, "D2"); v != "1.5" {
		t.Errorf("D2 = %q, want 1.5", v)
	}
}

func TestWorkbookRefusals(t *testing.T) {
	if _, err := buildWorkbook(nil, nil); err == nil {
		t.Error("empty columns should refuse") // excelize would panic on A1 range
	}
	cols := []WorkbookColumn{{Name: "a", Type: "int"}}
	if _, err := buildWorkbook(cols, [][]interface{}{{1.0, "extra"}}); err == nil {
		t.Error("a ragged row must be refused")
	}
	if !isDateColumn("timestamp with time zone") || isDateColumn("varchar") {
		t.Error("date type detection is wrong")
	}
	if _, ok := parseWorkbookTime(time.RFC3339); ok {
		t.Error("an empty string is not a date")
	}
}

// TestTableIndexesSQLite drives the index reader against a real SQLite file:
// declared indexes come back with their columns, implicit autoindexes do not.
func TestTableIndexesSQLite(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	db, err := createConnection(ConnConfig{Type: dbTypeSQLite, Database: dbFile})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT UNIQUE)`,
		`CREATE INDEX idx_users_name ON users(name)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	db.Close()

	c := &Client{live: make(map[string]*Conn), fkCache: make(map[string][]ForeignKeyInfo)}
	if err := c.connect(ConnConfig{ID: "c0", Name: "t", Type: dbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })

	indexes, err := c.tableIndexes("c0", dbFile, "", "users")
	if err != nil {
		t.Fatalf("tableIndexes: %v", err)
	}
	if len(indexes) != 1 {
		t.Fatalf("indexes = %+v, want only idx_users_name", indexes)
	}
	idx := indexes[0]
	if idx.Name != "idx_users_name" || idx.Unique || idx.Primary {
		t.Errorf("index = %+v, want non-unique idx_users_name", idx)
	}
	if len(idx.Columns) != 1 || idx.Columns[0] != "name" {
		t.Errorf("index columns = %v, want [name]", idx.Columns)
	}
}

// TestAggregateIndexes checks the flat-to-structured fold, including the
// multi-column case keeping catalog order.
func TestAggregateIndexes(t *testing.T) {
	indexes := aggregateIndexes([]indexRow{
		{name: "idx_ab", kind: "BTREE", column: "a", unique: true},
		{name: "idx_ab", kind: "BTREE", column: "b", unique: true},
		{name: "idx_c", kind: "HASH", column: "c"},
	})
	if len(indexes) != 2 {
		t.Fatalf("indexes = %d, want 2", len(indexes))
	}
	if indexes[0].Name != "idx_ab" || !indexes[0].Unique || len(indexes[0].Columns) != 2 ||
		indexes[0].Columns[0] != "a" || indexes[0].Columns[1] != "b" {
		t.Errorf("aggregated index = %+v", indexes[0])
	}
	if indexes[1].Kind != "HASH" || indexes[1].Unique {
		t.Errorf("second index = %+v", indexes[1])
	}
}
