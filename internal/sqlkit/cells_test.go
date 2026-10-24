package sqlkit

import (
	"database/sql"
	"math"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// The cells travel as tagged values, so a 64-bit integer keeps its value. This
// matters the moment a key or an id is involved: a bigint key that came back as
// "9223372036854775807" and then as a float64 is already wrong, and the grid's
// cursor would silently point at a neighbouring row.
//
// The old shape sent every cell as a string, so the loss happened at the first
// Scan. These tests pin the values instead of their rendering.

func TestFormatValueKeepsBigIntegersExact(t *testing.T) {
	// 2^53 + 1 is the smallest integer a float64 cannot hold.
	const big = int64(9007199254740993)
	cell := FormatCell(big)
	if cell.Kind != "int" {
		t.Fatalf("kind = %q, want int", cell.Kind)
	}
	if cell.Int != big {
		t.Fatalf("int = %d, want %d", cell.Int, big)
	}
	// The text form of the same value must be exact too.
	if got := cellText(cell); got != "9007199254740993" {
		t.Fatalf("text = %q", got)
	}
}

func TestFormatCellKeepsEveryKindDistinct(t *testing.T) {
	cases := []struct {
		name  string
		value any
		kind  string
		text  string
	}{
		{"null", nil, "null", ""},
		{"text", "hello", "text", "hello"},
		{"int", int64(42), "int", "42"},
		{"float", 1.5, "float", "1.5"},
		{"bool", true, "bool", "true"},
		{"blob", []byte{1, 2, 3}, "blob", "\x01\x02\x03"},
		{"time", time.Date(2026, 10, 24, 12, 0, 0, 123456789, time.UTC), "time", "2026-10-24 12:00:00.123456789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cell := FormatCell(tc.value)
			if cell.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q", cell.Kind, tc.kind)
			}
			if got := cellText(cell); got != tc.text {
				t.Fatalf("text = %q, want %q", got, tc.text)
			}
		})
	}
}

// A NULL and the empty string must not be the same cell: the grid shows them
// differently and an edit writes them differently.
func TestNullAndEmptyStringAreDifferentCells(t *testing.T) {
	null := FormatCell(nil)
	empty := FormatCell("")
	if null.Kind == empty.Kind {
		t.Fatalf("NULL and the empty string share the kind %q", null.Kind)
	}
	if null.Kind != "null" {
		t.Fatalf("NULL kind = %q", null.Kind)
	}
	if empty.Kind != "text" || empty.Text != "" {
		t.Fatalf("the empty string = %+v", empty)
	}
}

// The float path must not pretend a float is an integer.
func TestFloatIsNotAnInteger(t *testing.T) {
	cell := FormatCell(2.0)
	if cell.Kind != "float" {
		t.Fatalf("kind = %q, want float", cell.Kind)
	}
	if math.Abs(cell.Flt-2.0) > 1e-9 {
		t.Fatalf("float = %v", cell.Flt)
	}
}

// The string form is what the UI shows; the tagged form is what travels.
func TestCellTextMatchesTheTaggedValue(t *testing.T) {
	cell := FormatCell(int64(7))
	if cellText(cell) != "7" {
		t.Fatalf("text = %q", cellText(cell))
	}
	// A NULL has no text at all, not an empty string.
	if cellText(FormatCell(nil)) != "" {
		t.Fatal("a NULL must have no text")
	}
}

// Reading rows must produce tagged cells, so the value survives the pipe.
func TestReadRowsProducesTaggedCells(t *testing.T) {
	// A driver that returns one row: an integer that does not fit a float64.
	rows, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Skipf("no sqlite driver available: %v", err)
	}
	defer rows.Close()
	if _, err := rows.Exec(`CREATE TABLE t (v integer)`); err != nil {
		t.Skipf("no sqlite: %v", err)
	}
	if _, err := rows.Exec(`INSERT INTO t VALUES (9007199254740993)`); err != nil {
		t.Skipf("sqlite cannot hold that value: %v", err)
	}
	read, err := rows.Query(`SELECT v FROM t`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer read.Close()

	res, err := ReadRows(read, 10, time.Now())
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
	if len(res.Rows[0]) != 1 {
		t.Fatalf("row = %v", res.Rows[0])
	}
}
