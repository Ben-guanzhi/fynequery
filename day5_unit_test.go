package main

// Day-5 unit tests: Safe Mode gating, keyring round-trip, and the ER-diagram
// model helpers.

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
)

// ---- safe mode ----------------------------------------------------------------

func TestSafeModeNameRoundTrip(t *testing.T) {
	for level, label := range safeModeNames {
		if got := safeModeForName(label); got != level {
			t.Errorf("safeModeForName(%q) = %d, want %d", label, got, level)
		}
		if got := safeModeName(level); got != label {
			t.Errorf("safeModeName(%d) = %q, want %q", level, got, label)
		}
	}
	if safeModeName(-1) != "Silent" || safeModeName(9) != "Silent" {
		t.Error("out-of-range levels should fall back to Silent")
	}
	if safeModeForName("nonsense") != 0 {
		t.Error("unknown labels should map to level 0")
	}
}

func TestIsReadOnlySQL(t *testing.T) {
	reads := []string{
		"SELECT 1", "  select * from t", "SHOW TABLES", "explain select 1",
		"DESCRIBE t", "PRAGMA table_info(t)", "WITH c AS (SELECT 1) SELECT * FROM c",
		"TABLE t", "VALUES (1)", "SELECT(select 1)",
	}
	for _, s := range reads {
		if !isReadOnlySQL(s) {
			t.Errorf("isReadOnlySQL(%q) = false, want true", s)
		}
	}
	writes := []string{
		"", "UPDATE t SET a=1", "DELETE FROM t", "INSERT INTO t VALUES (1)",
		"DROP TABLE t", "TRUNCATE t", "SET foreign_key_checks=0",
	}
	for _, s := range writes {
		if isReadOnlySQL(s) {
			t.Errorf("isReadOnlySQL(%q) = true, want false", s)
		}
	}
}

func TestSafeGateBlocked(t *testing.T) {
	conn := Connection{SafeMode: 0}
	if safeGateBlocked(conn, "DROP TABLE t") {
		t.Error("Silent mode must never block")
	}
	conn.SafeMode = 1
	for _, s := range []string{"SELECT 1", "DROP TABLE t"} {
		if !safeGateBlocked(conn, s) {
			t.Errorf("Alert All must block %q", s)
		}
	}
	conn.SafeMode = 2
	if safeGateBlocked(conn, "SELECT 1") {
		t.Error("Alert Write must not block reads")
	}
	if !safeGateBlocked(conn, "DROP TABLE t") {
		t.Error("Alert Write must block writes")
	}
	conn.SafeMode = 3
	if !safeGateBlocked(conn, "SELECT 1") {
		t.Error("Password All must block reads")
	}
	conn.SafeMode = 4
	if safeGateBlocked(conn, "SELECT 1") {
		t.Error("Password Write must not block reads")
	}
	if !safeGateBlocked(conn, "DELETE FROM t") {
		t.Error("Password Write must block writes")
	}
}

func TestTruncateForDialog(t *testing.T) {
	if got := truncateForDialog("short"); got != "short" {
		t.Errorf("short input should pass through, got %q", got)
	}
	long := strings.Repeat("x", 600)
	got := truncateForDialog(long)
	if n := len([]rune(got)); n != 501 || !strings.HasSuffix(got, "…") {
		t.Errorf("long input should be cut to 500 runes + ellipsis, got %d runes", n)
	}
}

// ---- keyring ------------------------------------------------------------------

func TestKeyringRoundTrip(t *testing.T) {
	if !keyringAvailable() {
		t.Skip("OS keyring not available on this platform")
	}
	name := "fynequery-unit-test"
	if err := keyringSet(name, "s3cret-值"); err != nil {
		t.Fatalf("keyringSet: %v", err)
	}
	defer keyringDelete(name)
	got, ok := keyringGet(name)
	if !ok {
		t.Fatal("keyringGet: entry not found after set")
	}
	if got != "s3cret-值" {
		t.Errorf("keyringGet = %q, want %q", got, "s3cret-值")
	}
	if err := keyringDelete(name); err != nil {
		t.Fatalf("keyringDelete: %v", err)
	}
	if _, ok := keyringGet(name); ok {
		t.Error("reading a deleted entry should fail")
	}
}

// ---- ER diagram ----------------------------------------------------------------

func TestERBoxSize(t *testing.T) {
	if got := erBoxSize(erTable{Cols: []erCol{{Name: "a"}, {Name: "b"}, {Name: "c"}}}); got.Height != erdTitleH+3*erdRowH {
		t.Errorf("3 columns => header + 3 rows, got %v", got.Height)
	}
	if got := erBoxSize(erTable{}); got.Height != erdTitleH+2*erdRowH {
		t.Errorf("0 columns should still show 2 rows, got %v", got.Height)
	}
}

func TestLayoutERModel(t *testing.T) {
	m := &erModel{Tables: []erTable{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}}
	layoutERModel(m)
	// 3 per row: first three share a Y, the fourth wraps to the next row.
	if m.Tables[0].Pos.Y != m.Tables[1].Pos.Y || m.Tables[1].Pos.Y != m.Tables[2].Pos.Y {
		t.Error("first row should be aligned")
	}
	if m.Tables[3].Pos.Y <= m.Tables[0].Pos.Y {
		t.Error("4th table should wrap to the next row")
	}
	if m.Tables[1].Pos.X-m.Tables[0].Pos.X != erdBoxW+erdGap {
		t.Errorf("column pitch should be box+gap, got %v", m.Tables[1].Pos.X-m.Tables[0].Pos.X)
	}
}

func TestERFKQuery(t *testing.T) {
	for _, d := range []string{"mysql", "mariadb"} {
		if q := erFKQuery(d); !strings.Contains(q, "DATABASE()") || !strings.Contains(q, "KEY_COLUMN_USAGE") {
			t.Errorf("mysql FK query should scope to the current schema: %q", q)
		}
	}
	for _, d := range []string{"postgres", "postgresql"} {
		if q := erFKQuery(d); !strings.Contains(q, "current_schema()") {
			t.Errorf("pg FK query should scope to the current schema: %q", q)
		}
	}
	if erFKQuery("sqlite") != "" {
		t.Error("sqlite has no FK metadata query")
	}
}

func TestERModelColumnsByName(t *testing.T) {
	m := &erModel{Tables: []erTable{{Name: "t", Cols: []erCol{{Name: "id", PK: true}, {Name: "ref_id"}}}}}
	cols := m.ColumnsByName("t")
	if len(cols) != 2 || !cols[0].PK || cols[1].Name != "ref_id" {
		t.Errorf("ColumnsByName(t) = %+v", cols)
	}
	if m.ColumnsByName("missing") != nil {
		t.Error("unknown table should return nil")
	}
}

// ---- misc day-5 helpers --------------------------------------------------------

func TestStatusColorPresets(t *testing.T) {
	for name, c := range statusColorPresets {
		if c == "" && name != "None" {
			t.Errorf("status color %q must be a non-empty hex value", name)
		}
	}
	if len(statusColorNames) == 0 || statusColorNames[0] != "None" {
		t.Error("status color list should start with None")
	}
}

func TestERDragDeltaTypes(t *testing.T) {
	// Guard the geometry math used while dragging a table box: position plus
	// delta must stay float32 and stay inside a positive quadrant.
	p := fyne.NewPos(10, 20)
	e := fyne.DragEvent{}
	e.Dragged = fyne.NewDelta(5, -7)
	got := fyne.NewPos(p.X+e.Dragged.DX, p.Y+e.Dragged.DY)
	if got.X != 15 || got.Y != 13 {
		t.Errorf("drag math = %v", got)
	}
}
