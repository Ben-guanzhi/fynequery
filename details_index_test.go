package main

import (
	"testing"

	"github.com/dbcore/dbcore"
)

// indexTableData turns kernel metadata into the Indexes page grid: one row per
// (index, column), with the normalized kind instead of engine-specific text.
func TestIndexTableDataFlattensCompositeIndex(t *testing.T) {
	infos := []dbcore.IndexInfo{
		{Name: "PRIMARY", Kind: dbcore.IndexKindPrimary, Unique: true, Primary: true, Columns: []string{"id"}},
		{Name: "idx_name_age", Kind: dbcore.IndexKindIndex, Columns: []string{"name", "age"}},
	}
	data := indexTableData(infos)
	if data == nil {
		t.Fatal("nil table")
	}
	if len(data.Cols) != 5 {
		t.Fatalf("cols = %d, want 5", len(data.Cols))
	}
	if len(data.Rows) != 3 {
		t.Fatalf("rows = %d, want 3 (1 + 2 columns)", len(data.Rows))
	}
	if data.Rows[0][0] == nil || *data.Rows[0][0] != "PRIMARY" || *data.Rows[0][3] != "YES" {
		t.Fatalf("primary row = %v", data.Rows[0])
	}
	if got := *data.Rows[2][2]; got != "age" {
		t.Fatalf("second column of composite index = %q, want age", got)
	}
	if got := *data.Rows[2][1]; got != "2" {
		t.Fatalf("SEQ_IN_INDEX = %q, want 2", got)
	}
	if got := *data.Rows[1][3]; got != "NO" {
		t.Fatalf("non-unique index marked unique: %q", got)
	}
	if data.Fetched != 3 || data.Total != 3 {
		t.Fatalf("counters = %d/%d", data.Fetched, data.Total)
	}
}

// A session without the capability must yield an empty page rather than panic
// or fall back to engine-specific SQL.
func TestFetchIndexesWithoutCapabilityReturnsNil(t *testing.T) {
	if got := fetchIndexes(nil, "app", "t"); got != nil {
		t.Fatalf("nil session should render nothing, got %+v", got)
	}
}
