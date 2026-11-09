package main

import (
	"strings"
	"testing"
)

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
	findNow(g, "li") // Alice(0,0) + Lima(3,0)
	if len(g.findHits) != 2 {
		t.Fatalf("hits = %v, want 2", g.findHits)
	}
	g.sortData(0) // Alice, Bob, Carol, Lima
	if len(g.findHits) != 2 || g.findHits[0] != (searchHitKey{0, 0}) || g.findHits[1] != (searchHitKey{3, 0}) {
		t.Fatalf("hits after sort = %v, want [{0 0} {3 0}]", g.findHits)
	}
}

// TestSortDataExactBeyondDouble pins the precision contract: a numeric column
// carrying values past float64's 53-bit mantissa must still order by VALUE.
// ParseFloat collapsed 2^53+1 and 2^53+2 to the same double, so the sort
// treated distinct rows as ties; the exact comparator keeps them apart. Also
// covers the mixed case (one value inside the double range, one beyond) and
// negatives.
func TestSortDataExactBeyondDouble(t *testing.T) {
	s := str
	data := &TableData{
		Cols: []ColumnInfo{{Name: "amount", Type: "decimal(38,0)"}},
		Rows: [][]*string{
			{s("9007199254740992")},  // 2^53 — exactly representable
			{s("9007199254740993")},  // 2^53+1 — rounds to 2^53
			{s("9007199254740994")},  // 2^53+2 — rounds to 2^53+2
			{s("-9007199254740993")}, // negative of an odd > 2^53
			{s("9007199254740991")},  // 2^53-1 — exactly representable
		},
	}
	g := newDataTable(data, nil)
	g.sortData(0)
	got := []string{
		deref(g.data.Rows[0][0]), deref(g.data.Rows[1][0]), deref(g.data.Rows[2][0]),
		deref(g.data.Rows[3][0]), deref(g.data.Rows[4][0]),
	}
	want := []string{
		"-9007199254740993",
		"9007199254740991",
		"9007199254740992",
		"9007199254740993",
		"9007199254740994",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("exact asc row %d = %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}

	// The big 38-digit magnitudes the wire path carries for DECIMAL(38).
	huge := &TableData{
		Cols: []ColumnInfo{{Name: "amount", Type: "decimal(38,0)"}},
		Rows: [][]*string{
			{s("12345678901234567890123456789012345678")},
			{s("12345678901234567890123456789012345679")},
			{s("1234567890123456789012345678901234567.8")}, // shorter integer part sorts first
		},
	}
	h := newDataTable(huge, nil)
	h.sortData(0)
	hgot := []string{deref(h.data.Rows[0][0]), deref(h.data.Rows[1][0]), deref(h.data.Rows[2][0])}
	if hgot[0] != "1234567890123456789012345678901234567.8" ||
		hgot[1] != "12345678901234567890123456789012345678" ||
		hgot[2] != "12345678901234567890123456789012345679" {
		t.Fatalf("38-digit asc = %v", hgot)
	}
}

// TestCompareExact pins the exact decimal comparator's units: sign, integer
// length, digit-lexical, fraction — and the zero edge (leading zeros fold to
// the same integer).
func TestCompareExact(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1", "2", -1},
		{"2", "10", -1}, // integer length before digits
		{"9", "10", -1},
		{"10", "9", 1},
		{"0", "0", 0},
		{"0", "00", 0},      // leading zeros fold to the same integer
		{"1.5", "1.50", 0},  // trailing zeros are spelling, not value
		{"1.5", "1.6", -1},
		{"12.50", "12.5", 0},
		{"-2", "-10", 1}, // negatives mirror
		{"-2", "2", -1},
		{"9007199254740993", "9007199254740994", -1},
	}
	for _, tc := range cases {
		if got := compareExact(tc.a, tc.b); got != tc.want {
			t.Errorf("compareExact(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// Scientific-notation text must sort by VALUE through the float path, not by
// the exact comparator (which parses plain decimals only): "1e3" has no dot,
// so its "integer part" would literally be the string "1e3" and "1e3" vs
// "1000" would order wrong.
func TestSortDataScientificNotationSortsByValue(t *testing.T) {
	s := str
	data := &TableData{
		Cols: []ColumnInfo{{Name: "n", Type: "double"}},
		Rows: [][]*string{
			{s("1e3")},   // 1000
			{s("1000")},  // 1000 — same value, different spelling
			{s("999")},
			{s("2.5e3")}, // 2500
		},
	}
	g := newDataTable(data, nil)
	g.sortData(0)
	got := []string{
		deref(g.data.Rows[0][0]), deref(g.data.Rows[1][0]),
		deref(g.data.Rows[2][0]), deref(g.data.Rows[3][0]),
	}
	want := []string{"999", "1e3", "1000", "2.5e3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sci-notation asc row %d = %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestSortDataExactVsJunkText 回归:exact 路径曾把非数字文本直接喂给
// compareExact(违反其"入参均为数字形状"的契约),构造出不可传递比较器——
// less(T,N)=true、less(N,E)=true、less(E,T)=true 成环,顺序随元素位置漂移。
// 现在 exact 仅在双侧均为数字时启用,垃圾文本回落字符串序。
func TestSortDataExactVsJunkText(t *testing.T) {
	s := str
	junk := strings.Repeat("!", 24) // 字节序上小于任何数字字符
	data := &TableData{
		Cols: []ColumnInfo{{Name: "v", Type: "int"}},
		Rows: [][]*string{
			{s("9007199254740993")}, // ParseFloat 往返失真 → exact
			{s(junk)},               // 非数字文本
			{s("5")},                // 普通 float64
		},
	}
	g := newDataTable(data, nil)
	g.sortData(0)
	got := []string{deref(g.data.Rows[0][0]), deref(g.data.Rows[1][0]), deref(g.data.Rows[2][0])}
	want := []string{junk, "5", "9007199254740993"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("asc with junk text row %d = %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
	// 翻转降序同样确定(旧代码在环下顺序依赖元素位置)。
	g.sortData(0)
	got = []string{deref(g.data.Rows[0][0]), deref(g.data.Rows[1][0]), deref(g.data.Rows[2][0])}
	want = []string{"9007199254740993", "5", junk}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("desc with junk text row %d = %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}
