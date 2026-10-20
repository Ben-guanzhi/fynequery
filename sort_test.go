package main

import "testing"

// TestSortGrid verifies column sorting: numeric-aware, NULLs always last,
// toggling direction on repeated taps.
func TestSortGrid(t *testing.T) {
	newRows := func() [][]string {
		return [][]string{
			{"10", "banana", "NULL"},
			{"2", "apple", "x"},
			{"1", "cherry", "a"},
			{"NULL", "zebra", "b"},
		}
	}
	// The raw rows behind the display: the "NULL" in column 0 of the last
	// row is a real SQL NULL (nil) and is what the sort sinks to the
	// bottom; the "NULL" text in column 2 of the first row is data.
	newRaw := func() [][]interface{} {
		return [][]interface{}{
			{int64(10), "banana", "NULL"},
			{int64(2), "apple", "x"},
			{int64(1), "cherry", "a"},
			{nil, "zebra", "b"},
		}
	}

	// Numeric ascending on column 0.
	c := &Client{grid: gridState{header: []string{"n", "s", "t"}, rows: newRows(), lastRows: newRaw(), sortCol: -1}}
	c.sortGrid(0)
	if !c.grid.sortAsc || c.grid.sortCol != 0 {
		t.Fatalf("expected asc sort col 0, got col=%d asc=%v", c.grid.sortCol, c.grid.sortAsc)
	}
	got := []string{c.grid.rows[0][0], c.grid.rows[1][0], c.grid.rows[2][0], c.grid.rows[3][0]}
	if got[0] != "1" || got[1] != "2" || got[2] != "10" || got[3] != "NULL" {
		t.Fatalf("numeric asc + NULL last: %v", got)
	}
	// The raw rows must follow the displayed order, or the edit queue's
	// WHERE clauses would point at the wrong rows.
	if c.grid.lastRows[3][0] != nil {
		t.Fatalf("the real NULL no longer sorts last in the raw rows: %v", c.grid.lastRows)
	}
	if c.grid.lastRows[0][0] != int64(1) {
		t.Fatalf("raw rows out of step with the display: %v", c.grid.lastRows)
	}

	// Toggle to descending.
	c.sortGrid(0)
	if c.grid.sortAsc {
		t.Fatalf("expected desc after second tap")
	}
	got = []string{c.grid.rows[0][0], c.grid.rows[1][0], c.grid.rows[2][0], c.grid.rows[3][0]}
	if got[0] != "10" || got[1] != "2" || got[2] != "1" || got[3] != "NULL" {
		t.Fatalf("numeric desc + NULL last: %v", got)
	}

	// String column ascending.
	c2 := &Client{grid: gridState{header: []string{"n", "s", "t"}, rows: newRows(), sortCol: -1}}
	c2.sortGrid(1)
	got = []string{c2.grid.rows[0][1], c2.grid.rows[1][1], c2.grid.rows[2][1], c2.grid.rows[3][1]}
	if got[0] != "apple" || got[1] != "banana" || got[2] != "cherry" || got[3] != "zebra" {
		t.Fatalf("string asc: %v", got)
	}
}

// TestSortGridTextColumnStaysLexicographic pins the type-aware half of the
// comparison: a column whose declared type is text keeps digits in string
// order ("10" < "2"), which is what a reader of a text column expects;
// the numeric path is reserved for numeric types and the typeless fallback.
func TestSortGridTextColumnStaysLexicographic(t *testing.T) {
	newRows := func() [][]string {
		return [][]string{
			{"10"}, {"2"}, {"1"}, {"abc"},
		}
	}

	c := &Client{grid: gridState{header: []string{"code"}, rows: newRows(),
		colTypes: []string{"varchar(20)"}, sortCol: -1}}
	c.sortGrid(0)
	got := []string{c.grid.rows[0][0], c.grid.rows[1][0], c.grid.rows[2][0], c.grid.rows[3][0]}
	if got[0] != "1" || got[1] != "10" || got[2] != "2" || got[3] != "abc" {
		t.Fatalf("text column asc = %v, want lexicographic [1 10 2 abc]", got)
	}

	// A numeric type keeps the numeric order the bare test pins.
	c2 := &Client{grid: gridState{header: []string{"n"}, rows: newRows(),
		colTypes: []string{"int"}, sortCol: -1}}
	c2.sortGrid(0)
	got = []string{c2.grid.rows[0][0], c2.grid.rows[1][0], c2.grid.rows[2][0], c2.grid.rows[3][0]}
	if got[0] != "1" || got[1] != "2" || got[2] != "10" || got[3] != "abc" {
		t.Fatalf("numeric column asc = %v, want [1 2 10 abc]", got)
	}
}
