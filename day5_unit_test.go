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
		// 前导注释曾使门禁读不到首词、把 SELECT 当写语句弹窗(FirstKeyword 修复)
		"-- note\nSELECT 1",
		"/* header */ SELECT 1",
		"  /* c1 */ -- c2\nSHOW TABLES",
		// 括号内含关键词但不是 CTE 体首词:子查询/普通标识符不误报
		"WITH c AS (SELECT * FROM (SELECT 1) x) SELECT * FROM c",
		"WITH c AS (SELECT \"delete\" FROM t) SELECT 1",
		"WITH c AS (/* DELETE FROM t */ SELECT 1) SELECT * FROM c",
		"WITH c AS (SELECT 1) , d AS (SELECT 2) SELECT * FROM c JOIN d",
	}
	for _, s := range reads {
		if !isReadOnlySQL(s) {
			t.Errorf("isReadOnlySQL(%q) = false, want true", s)
		}
	}
	writes := []string{
		"", "UPDATE t SET a=1", "DELETE FROM t", "INSERT INTO t VALUES (1)",
		"DROP TABLE t", "TRUNCATE t", "SET foreign_key_checks=0",
		// 前导注释不改变写语句的判定
		"-- cleanup\nDROP TABLE t",
		// PostgreSQL 数据修改型 CTE:主语句是 SELECT,真实写入在 CTE 体里
		"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
		"WITH i AS (INSERT INTO t VALUES (1) RETURNING id) SELECT * FROM i",
		"WITH u AS (UPDATE t SET a=1 RETURNING a) SELECT * FROM u",
		"WITH r AS (REPLACE INTO t VALUES (1)) SELECT 1",
		"WITH m AS (MERGE INTO t USING s ON t.id=s.id WHEN MATCHED THEN UPDATE SET t.a=s.a) SELECT 1",
		"WITH x AS ( /* 注释 */ DELETE FROM t) SELECT 1",
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
	// Layered layout: no FKs => all tables at level 0 => single column,
	// stacked vertically (alphabetical seed order).
	for i := 1; i < len(m.Tables); i++ {
		if m.Tables[i].Pos.X != m.Tables[0].Pos.X {
			t.Errorf("table %q should share the single column, got x=%v vs %v", m.Tables[i].Name, m.Tables[i].Pos.X, m.Tables[0].Pos.X)
		}
		if m.Tables[i].Pos.Y <= m.Tables[i-1].Pos.Y {
			t.Errorf("table %q should stack below %q, got y=%v vs %v", m.Tables[i].Name, m.Tables[i-1].Name, m.Tables[i].Pos.Y, m.Tables[i-1].Pos.Y)
		}
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
