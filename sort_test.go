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

	// Numeric ascending on column 0.
	c := &Client{grid: gridState{header: []string{"n", "s", "t"}, rows: newRows(), sortCol: -1}}
	c.sortGrid(0)
	if !c.grid.sortAsc || c.grid.sortCol != 0 {
		t.Fatalf("expected asc sort col 0, got col=%d asc=%v", c.grid.sortCol, c.grid.sortAsc)
	}
	got := []string{c.grid.rows[0][0], c.grid.rows[1][0], c.grid.rows[2][0], c.grid.rows[3][0]}
	if got[0] != "1" || got[1] != "2" || got[2] != "10" || got[3] != "NULL" {
		t.Fatalf("numeric asc + NULL last: %v", got)
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
