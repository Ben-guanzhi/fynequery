package sqlkit

// Table-driven coverage for the statement executor shared by the built-in
// MySQL driver and every plugin: Run / StreamRun / ReadRows / FormatValue /
// IsRowReturning. modernc.org/sqlite's in-memory engine stands in for a real
// server; the assertions target sqlkit's own contract (row caps, batch
// splitting, cell kinds), not SQLite behaviour.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"database/sql"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"fynequery/proto"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	h, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	// One connection keeps the in-memory database shared across calls.
	h.SetMaxOpenConns(1)
	if _, err := h.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT, f REAL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`INSERT INTO t (id, v, f) VALUES (1, 'a', 1.5), (2, 'b', 2.5), (3, NULL, NULL)`); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestIsRowReturning(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"SELECT 1", true},
		{"  select 1", true},
		{"WITH x AS (SELECT 1) SELECT * FROM x", true},
		{"SHOW TABLES", true},
		{"DESC t", true},
		{"PRAGMA table_info(t)", true},
		{"TABLE t", true},
		{"VALUES (1)", true},
		{"INSERT INTO t VALUES (1)", false},
		{"UPDATE t SET v='x'", false},
		{"DELETE FROM t", false},
		{"CREATE TABLE x (a INT)", false},
		{"BEGIN", false},
		{"-- comment\nSELECT 1", false}, // leading comment: first word is not SELECT
		{"", false},
	}
	for _, tc := range cases {
		if got := IsRowReturning(tc.sql); got != tc.want {
			t.Errorf("IsRowReturning(%q) = %v, want %v", tc.sql, got, tc.want)
		}
	}
}

func TestRunSelect(t *testing.T) {
	h := testDB(t)

	// Row-returning: capped at maxRows, cells keep their kinds.
	res, err := Run(h, "SELECT id, v, f FROM t ORDER BY id", 3)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(res.Rows))
	}
	if len(res.Columns) != 3 || res.Columns[0].Name != "id" {
		t.Fatalf("columns = %+v", res.Columns)
	}
	if res.Rows[0][0].Kind != "int" || res.Rows[0][0].Int != 1 {
		t.Fatalf("cell(0,0) = %+v, want int 1", res.Rows[0][0])
	}
	if res.Rows[0][1].Kind != "text" || res.Rows[0][1].Text != "a" {
		t.Fatalf("cell(0,1) = %+v, want text a", res.Rows[0][1])
	}
	if res.Rows[2][1].Kind != "null" || res.Rows[2][2].Kind != "null" {
		t.Fatalf("NULL cells = %+v / %+v, want null kind", res.Rows[2][1], res.Rows[2][2])
	}
	if res.DurationMS < 0 {
		t.Fatalf("DurationMS = %d", res.DurationMS)
	}
	if res.RowsAffected != 0 {
		t.Fatalf("SELECT RowsAffected = %d, want 0", res.RowsAffected)
	}

	// maxRows caps the scan: only the first 2 rows come back.
	res, err = Run(h, "SELECT id FROM t ORDER BY id", 2)
	if err != nil {
		t.Fatalf("Run(maxRows=2): %v", err)
	}
	if len(res.Rows) != 2 || res.Rows[1][0].Int != 2 {
		t.Fatalf("capped rows = %+v, want ids 1..2", res.Rows)
	}

	// maxRows <= 0 falls back to the 1000 default (all 3 rows come back).
	res, err = Run(h, "SELECT id FROM t", 0)
	if err != nil {
		t.Fatalf("Run(maxRows=0): %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %d, want 3 (default cap)", len(res.Rows))
	}
}

func TestRunExec(t *testing.T) {
	h := testDB(t)

	res, err := Run(h, "UPDATE t SET v = 'x' WHERE id <= 2", 10)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RowsAffected != 2 {
		t.Fatalf("RowsAffected = %d, want 2", res.RowsAffected)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("Exec produced %d rows, want 0", len(res.Rows))
	}

	if _, err := Run(h, "UPDATE nope SET x = 1", 10); err == nil {
		t.Fatal("expected error from a failing statement")
	}
}

func TestReadRowsKinds(t *testing.T) {
	h := testDB(t)
	rows, err := h.Query("SELECT id, v, f FROM t WHERE id = 3")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	res, err := ReadRows(rows, 10, time.Now())
	if err != nil {
		t.Fatalf("ReadRows: %v", err)
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 3 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if res.Rows[0][1].Kind != "null" || res.Rows[0][2].Kind != "null" {
		t.Fatalf("NULL cells = %+v / %+v, want null", res.Rows[0][1], res.Rows[0][2])
	}
}

func TestStreamRunBatches(t *testing.T) {
	h := testDB(t)
	if _, err := h.Exec(`INSERT INTO t (id, v) VALUES (4, 'd'), (5, 'e')`); err != nil {
		t.Fatal(err)
	}

	// 5 rows at batch=2 → 3 frames (2+2+1); columns ride the first frame only.
	var frames int
	var total int64
	var firstCols *[]proto.ColumnInfo
	var bareFrames int
	res, err := StreamRun(h, "SELECT id FROM t ORDER BY id", 2,
		func(cols []proto.ColumnInfo, rows [][]proto.CellPayload) error {
			frames++
			if cols != nil {
				if firstCols != nil {
					t.Fatal("columns emitted more than once")
				}
				firstCols = &cols
			} else {
				bareFrames++
			}
			total += int64(len(rows))
			return nil
		})
	if err != nil {
		t.Fatalf("StreamRun: %v", err)
	}
	if res.Rows != 5 || total != 5 {
		t.Fatalf("StreamRun rows = %d (emitted %d), want 5", res.Rows, total)
	}
	if frames != 3 {
		t.Fatalf("frames = %d, want 3", frames)
	}
	if firstCols == nil || len(*firstCols) != 1 || (*firstCols)[0].Name != "id" {
		t.Fatalf("first frame columns = %+v", firstCols)
	}
	if bareFrames == 0 {
		t.Fatal("expected frames without columns after the first")
	}

	// batch <= 0 falls back to the 200 default.
	var once int
	_, err = StreamRun(h, "SELECT id FROM t", 0, func(_ []proto.ColumnInfo, rows [][]proto.CellPayload) error {
		once++
		return nil
	})
	if err != nil || once != 1 {
		t.Fatalf("default batch: calls=%d err=%v, want 1 call", once, err)
	}
}

func TestStreamRunEmitErrorAborts(t *testing.T) {
	h := testDB(t)
	boom := errors.New("sink closed")
	_, err := StreamRun(h, "SELECT id FROM t", 2, func(_ []proto.ColumnInfo, _ [][]proto.CellPayload) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("StreamRun err = %v, want the emit error", err)
	}
}

func TestStreamRunExec(t *testing.T) {
	h := testDB(t)
	res, err := StreamRun(h, "UPDATE t SET v = 'y' WHERE id = 1", 2,
		func(_ []proto.ColumnInfo, _ [][]proto.CellPayload) error {
			t.Fatal("Exec statement must not emit rows")
			return nil
		})
	if err != nil {
		t.Fatalf("StreamRun: %v", err)
	}
	if res.RowsAffected != 1 || res.Rows != 0 {
		t.Fatalf("result = %+v, want RowsAffected=1 Rows=0", res)
	}
}

func TestFormatValue(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 34, 56, 123456789, time.UTC)
	f64 := 3.25
	f32 := float32(1.5)
	b := true
	i64 := int64(-7)
	raw := []byte{0x00, 0x01}
	t.Run("nil", func(t *testing.T) {
		if got := FormatValue(nil); got != nil {
			t.Fatalf("nil → %v, want nil", *got)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		got := FormatValue(raw)
		if got == nil || len(*got) != 2 {
			t.Fatalf("bytes → %v, want 2 raw bytes", got)
		}
	})
	t.Run("string", func(t *testing.T) {
		got := FormatValue("hi")
		if got == nil || *got != "hi" {
			t.Fatalf("string → %v", got)
		}
	})
	t.Run("time", func(t *testing.T) {
		got := FormatValue(now)
		if got == nil || *got != "2026-10-26 12:34:56.123456789" {
			t.Fatalf("time → %v, want fractional seconds kept", got)
		}
	})
	t.Run("ints", func(t *testing.T) {
		if got := FormatValue(i64); *got != "-7" {
			t.Fatalf("int64 → %v", *got)
		}
		if got := FormatValue(uint64(1)<<63 + 1); *got != strconvU64() {
			t.Fatalf("big uint64 → %v, want exact text", *got)
		}
	})
	t.Run("floats", func(t *testing.T) {
		if got := FormatValue(f64); *got != "3.25" {
			t.Fatalf("float64 → %v", *got)
		}
		if got := FormatValue(f32); *got != "1.5" {
			t.Fatalf("float32 → %v", *got)
		}
	})
	t.Run("bool", func(t *testing.T) {
		if got := FormatValue(b); *got != "true" {
			t.Fatalf("bool → %v", *got)
		}
	})
	t.Run("default", func(t *testing.T) {
		type custom struct{ n int }
		got := FormatValue(custom{n: 3})
		if got == nil || !strings.Contains(*got, "3") {
			t.Fatalf("default → %v", got)
		}
	})
}

func strconvU64() string {
	return "9223372036854775809"
}
