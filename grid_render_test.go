package main

import (
	"reflect"
	"testing"
)

// Guard for the grid's NULL handling: a real SQL NULL (a nil in the raw
// result) and the literal text "NULL" must not be treated the same, neither
// in the cell styling nor in the sort that sinks NULLs to the bottom.
func TestCellIsNullDistinguishesRealNullFromText(t *testing.T) {
	c := &Client{grid: gridState{
		lastRows: [][]interface{}{
			{nil, "NULL"},
			{int64(5), ""},
		},
	}}
	if !c.cellIsNull(0, 0) {
		t.Error("real NULL not detected")
	}
	if c.cellIsNull(0, 1) {
		t.Error("the text value \"NULL\" mistaken for a SQL NULL")
	}
	if c.cellIsNull(1, 1) {
		t.Error("an empty string mistaken for a SQL NULL")
	}
	if c.cellIsNull(5, 0) {
		t.Error("an out-of-range row reported as NULL")
	}
}

// Guard for the sort keeping the raw rows in step with the displayed rows:
// the edit queue and the NULL test address base rows by display index, so a
// sort that moved one and not the other would point WHERE clauses at a
// neighbouring row.
func TestSortGridKeepsRawRowsInSync(t *testing.T) {
	c := &Client{grid: gridState{
		header:   []string{"id (int)"},
		cols:     []string{"id"},
		colTypes: []string{"int"},
		edits:    newGridEditState(),
		rows:     [][]string{{"3"}, {"1"}, {"2"}},
		lastRows: [][]interface{}{{int64(3)}, {int64(1)}, {int64(2)}},
	}}
	c.sortGrid(0)
	if c.grid.rows[0][0] != "1" || c.grid.rows[2][0] != "3" {
		t.Errorf("rows not sorted: %v", c.grid.rows)
	}
	if c.grid.lastRows[0][0] != int64(1) || c.grid.lastRows[2][0] != int64(3) {
		t.Errorf("raw rows out of sync with the display: %v", c.grid.lastRows)
	}
}

// The text "NULL" is data and sorts lexicographically; only the real NULL
// sinks below it.
func TestSortGridSunksRealNullsNotTheTextNull(t *testing.T) {
	c := &Client{grid: gridState{
		header:   []string{"name"},
		cols:     []string{"name"},
		colTypes: []string{"text"},
		edits:    newGridEditState(),
		rows:     [][]string{{"NULL"}, {"2"}, {"NULL"}, {"1"}},
		lastRows: [][]interface{}{{"NULL"}, {int64(2)}, {nil}, {int64(1)}},
	}}
	c.sortGrid(0)
	want := [][]string{{"1"}, {"2"}, {"NULL"}, {"NULL"}}
	if !reflect.DeepEqual(c.grid.rows, want) {
		t.Errorf("rows = %v, want %v", c.grid.rows, want)
	}
	if c.grid.lastRows[2][0] != "NULL" {
		t.Errorf("the text NULL lost its rank: %v", c.grid.lastRows)
	}
	if c.grid.lastRows[3][0] != nil {
		t.Errorf("the real NULL no longer sorts last: %v", c.grid.lastRows)
	}
}

func TestAlignRightForNumbersOnly(t *testing.T) {
	got := alignRightFor([]string{"int", "text", "", "DECIMAL"})
	want := []bool{true, false, false, true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("alignRightFor = %v, want %v", got, want)
	}
}

func TestColAlignsRightIsSafeOnABareGrid(t *testing.T) {
	c := &Client{}
	if c.colAlignsRight(0) || c.colAlignsRight(-1) {
		t.Error("a grid without types must not right-align anything")
	}
}
