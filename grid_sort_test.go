package main

import "testing"

// deref returns the string value of a cell, "" for nil (SQL NULL).
func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// TestSortDataNumeric pins the sort contract: a numeric column orders by
// value (1 < 2 < 10), NULLs always sink to the bottom regardless of direction,
// and a second tap on the same column flips ascending/descending. Mirrors
// TablePlus HeaderSort / HeaderSortDirection.
func TestSortDataNumeric(t *testing.T) {
	s := str
	data := &TableData{
		Cols: []ColumnInfo{{Name: "n", Type: "int"}, {Name: "s", Type: "varchar"}, {Name: "t", Type: "varchar"}},
		Rows: [][]*string{
			{s("10"), s("banana"), nil},
			{s("2"), s("apple"), s("x")},
			{s("1"), s("cherry"), s("a")},
			{nil, s("zebra"), s("b")},
		},
	}
	g := newDataTable(data, nil)

	g.sortData(0)
	if !g.sortAsc || g.sortCol != 0 {
		t.Fatalf("expected asc sort col 0, got col=%d asc=%v", g.sortCol, g.sortAsc)
	}
	got := []string{deref(g.data.Rows[0][0]), deref(g.data.Rows[1][0]), deref(g.data.Rows[2][0]), deref(g.data.Rows[3][0])}
	if got[0] != "1" || got[1] != "2" || got[2] != "10" || got[3] != "" {
		t.Fatalf("numeric asc + NULL last: %v", got)
	}

	// Toggle to descending: NULL stays last.
	g.sortData(0)
	if g.sortAsc {
		t.Fatal("expected desc after second tap")
	}
	got = []string{deref(g.data.Rows[0][0]), deref(g.data.Rows[1][0]), deref(g.data.Rows[2][0]), deref(g.data.Rows[3][0])}
	if got[0] != "10" || got[1] != "2" || got[2] != "1" || got[3] != "" {
		t.Fatalf("numeric desc + NULL last: %v", got)
	}
}

// TestSortDataString checks a text column keeps lexical order.
func TestSortDataString(t *testing.T) {
	s := str
	data := &TableData{
		Cols: []ColumnInfo{{Name: "n", Type: "int"}, {Name: "s", Type: "varchar"}, {Name: "t", Type: "varchar"}},
		Rows: [][]*string{
			{s("10"), s("banana"), nil},
			{s("2"), s("apple"), s("x")},
			{s("1"), s("cherry"), s("a")},
			{nil, s("zebra"), s("b")},
		},
	}
	g := newDataTable(data, nil)
	g.sortData(1)
	got := []string{deref(g.data.Rows[0][1]), deref(g.data.Rows[1][1]), deref(g.data.Rows[2][1]), deref(g.data.Rows[3][1])}
	if got[0] != "apple" || got[1] != "banana" || got[2] != "cherry" || got[3] != "zebra" {
		t.Fatalf("string asc: %v", got)
	}
}

// TestSortDataTextColumnStaysLexicographic pins the type-aware half of the
// comparison: a column whose declared type is text keeps digits in string
// order ("10" < "2"), which is what a reader of a text column expects; the
// numeric path is reserved for numeric types.
func TestSortDataTextColumnStaysLexicographic(t *testing.T) {
	s := str
	mk := func(typ string) *dataTable {
		return newDataTable(&TableData{
			Cols: []ColumnInfo{{Name: "code", Type: typ}},
			Rows: [][]*string{{s("10")}, {s("2")}, {s("1")}, {s("abc")}},
		}, nil)
	}

	g := mk("varchar(20)")
	g.sortData(0)
	got := []string{deref(g.data.Rows[0][0]), deref(g.data.Rows[1][0]), deref(g.data.Rows[2][0]), deref(g.data.Rows[3][0])}
	if got[0] != "1" || got[1] != "10" || got[2] != "2" || got[3] != "abc" {
		t.Fatalf("text column asc = %v, want lexicographic [1 10 2 abc]", got)
	}

	g2 := mk("int")
	g2.sortData(0)
	got = []string{deref(g2.data.Rows[0][0]), deref(g2.data.Rows[1][0]), deref(g2.data.Rows[2][0]), deref(g2.data.Rows[3][0])}
	if got[0] != "1" || got[1] != "2" || got[2] != "10" || got[3] != "abc" {
		t.Fatalf("numeric column asc = %v, want [1 2 10 abc]", got)
	}
}

// TestSortDataResetsFind pins that reordering rows re-collects find hits so
// highlights keep pointing at the right cells (a sort invalidates positions).
func TestSortDataResetsFind(t *testing.T) {
	s := str
	g := newDataTable(&TableData{
		Cols: []ColumnInfo{{Name: "name", Type: "varchar"}},
		Rows: [][]*string{{s("Alice")}, {s("Bob")}, {s("Carol")}, {s("Lima")}},
	}, nil)
	g.setFindTerm("li") // Alice(0,0) + Lima(3,0)
	if len(g.findHits) != 2 {
		t.Fatalf("hits = %v, want 2", g.findHits)
	}
	g.sortData(0) // Alice, Bob, Carol, Lima
	if len(g.findHits) != 2 || g.findHits[0] != (searchHitKey{0, 0}) || g.findHits[1] != (searchHitKey{3, 0}) {
		t.Fatalf("hits after sort = %v, want [{0 0} {3 0}]", g.findHits)
	}
}
