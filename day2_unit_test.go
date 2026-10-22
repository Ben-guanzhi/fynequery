package main

// Unit tests for the day-2 feature cluster: Query Magics parsing,
// copy/IN-clause builders, commit preview SQL, query parameters and
// anchored quick-open matching.

import (
	"strings"
	"testing"

	"fynequery/db"
)

// --- Query Magics ---------------------------------------------------------

func TestSplitMagic(t *testing.T) {
	cases := []struct{ alias, base, spec string }{
		{"plain", "plain", ""},
		{"url__format__link", "url", "format:link"},
		{"price__format__money__gbp", "price", "format:money__gbp"},
		{"pct__format__progress__50", "pct", "format:progress__50"},
		{"user_id__goto__users__id", "user_id", "goto:users__id"},
		{"id__goto__users", "id", "goto:users"},
		{"x__format__unixtime__ms", "x", "format:unixtime__ms"},
	}
	for _, c := range cases {
		base, spec := splitMagic(c.alias)
		if base != c.base || spec != c.spec {
			t.Errorf("splitMagic(%q) = (%q,%q), want (%q,%q)", c.alias, base, spec, c.base, c.spec)
		}
	}
}

func TestGotoTarget(t *testing.T) {
	if table, col := gotoTarget("users__id"); table != "users" || col != "id" {
		t.Errorf("gotoTarget(users__id) = (%q,%q)", table, col)
	}
	if table, col := gotoTarget("orders"); table != "orders" || col != "" {
		t.Errorf("gotoTarget(orders) = (%q,%q)", table, col)
	}
}

func TestRenderMagicCheck(t *testing.T) {
	if txt, _, _, ok := renderMagic("format:check", "1", &lightPalette); !ok || txt != "✔" {
		t.Errorf("check(1) = %q ok=%v, want ✔", txt, ok)
	}
	if txt, _, _, ok := renderMagic("format:check", "0", &lightPalette); !ok || txt != "✘" {
		t.Errorf("check(0) = %q ok=%v, want ✘", txt, ok)
	}
}

func TestRenderMagicMoney(t *testing.T) {
	txt, _, _, ok := renderMagic("format:money__gbp", "12.5", &lightPalette)
	if !ok || txt != "GBP 12.50" {
		t.Errorf("money = %q ok=%v, want 'GBP 12.50'", txt, ok)
	}
}

func TestRenderMagicProgressStars(t *testing.T) {
	txt, _, _, ok := renderMagic("format:progress", "35", &lightPalette)
	if !ok || !strings.Contains(txt, "35/100") {
		t.Errorf("progress = %q ok=%v", txt, ok)
	}
	txt, _, _, ok = renderMagic("format:stars", "4", &lightPalette)
	if !ok || !strings.HasPrefix(txt, "★★★★") {
		t.Errorf("stars = %q ok=%v", txt, ok)
	}
}

func TestFormatUnixTime(t *testing.T) {
	// 1700000000 = 2023-11-14 22:13:20 UTC; local may shift the hour, so
	// only assert on the date.
	got := formatUnixTime("1700000000", nil)
	if !strings.HasPrefix(got, "2023-11-14") && !strings.HasPrefix(got, "2023-11-15") {
		t.Errorf("unixtime = %q", got)
	}
	got = formatUnixTime("1700000000000", []string{"ms"})
	if !strings.HasPrefix(got, "2023-11-14") && !strings.HasPrefix(got, "2023-11-15") {
		t.Errorf("unixtime ms = %q", got)
	}
	if got := formatUnixTime("not-a-number", nil); got != "not-a-number" {
		t.Errorf("non-numeric must pass through, got %q", got)
	}
}

// --- Copy As / IN clause ---------------------------------------------------

func newTestGrid(rows int, numericCol bool) *dataTable {
	cols := []ColumnInfo{{Name: "id"}, {Name: "name"}}
	if numericCol {
		cols[0].Type = "int"
	}
	rowsArr := make([][]*string, rows)
	for r := range rowsArr {
		a, b := "a"+string(rune('A'+r)), "n"+string(rune('A'+r))
		rowsArr[r] = []*string{&a, &b}
	}
	return newDataTable(&TableData{Cols: cols, Rows: rowsArr, Fetched: rows}, nil)
}

func TestRowCSVWithHeader(t *testing.T) {
	g := newTestGrid(2, false)
	out := g.rowCSV(0, true)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (header+row), got %d: %q", len(lines), out)
	}
	if lines[0] != `id,name` {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "aA") || !strings.Contains(lines[1], "nA") {
		t.Errorf("row line = %q", lines[1])
	}
}

func TestRowJSON(t *testing.T) {
	g := newTestGrid(1, false)
	out := g.rowJSON(0)
	if !strings.Contains(out, `"id":"aA"`) || !strings.Contains(out, `"name":"nA"`) {
		t.Errorf("rowJSON = %q", out)
	}
}

func TestRowMarkdown(t *testing.T) {
	g := newTestGrid(1, false)
	out := g.rowMarkdown(0)
	if !strings.Contains(out, "|id|name|") || !strings.Contains(out, "|---|---|") ||
		!strings.Contains(out, "|aA|nA|") {
		t.Errorf("rowMarkdown = %q", out)
	}
}

func TestRowInsert(t *testing.T) {
	g := newTestGrid(1, false)
	g.tableName = "users"
	out := g.rowInsert(0)
	if !strings.Contains(out, "INSERT INTO users") || !strings.Contains(out, "'aA'") {
		t.Errorf("rowInsert = %q", out)
	}
}

func TestInClauseNumericUnquoted(t *testing.T) {
	cols := []ColumnInfo{{Name: "id", Type: "int"}, {Name: "name"}}
	vals := [][]*string{{intS("1"), intS("x")}, {intS("2"), intS("y")}}
	g := &dataTable{data: &TableData{Cols: cols, Rows: vals}}
	out := g.inClause(0) // column "id" (numeric type, numeric values)
	if !strings.HasPrefix(out, "id IN (") {
		t.Fatalf("inClause = %q", out)
	}
	if !strings.Contains(out, "1, 2") {
		t.Errorf("numeric IN list should be unquoted: %q", out)
	}
}

func intS(s string) *string { return &s }

func TestInClauseTextQuoted(t *testing.T) {
	g := newTestGrid(3, false)
	out := g.inClause(1) // column "name"
	if !strings.HasPrefix(out, "name IN (") {
		t.Fatalf("inClause = %q", out)
	}
	if !strings.Contains(out, "'nA'") {
		t.Errorf("text IN values should be quoted: %q", out)
	}
}

// --- Commit preview --------------------------------------------------------

func TestBuildPreviewSQL(t *testing.T) {
	mk := func(s string) *string { return &s }
	updates := []rowUpdate{
		{pk: map[string]*string{"id": mk("1")},
			changes: map[string]*string{"name": mk("O'Hara"), "active": nil, "level": mk(db.DefaultMarker)}},
	}
	deletes := []map[string]*string{{"id": mk("9")}}
	inserts := []map[string]*string{{"name": mk("zoe"), "level": nil}}
	out := buildPreviewSQL("users", "mysql", updates, deletes, inserts)
	for _, want := range []string{
		"UPDATE `users`",
		"`name` = 'O''Hara'",
		"`active` = NULL",
		"`level` = DEFAULT",
		"`id` = '1'",
		"DELETE FROM `users` WHERE `id` = '9'",
		"INSERT INTO `users` (`level`, `name`) VALUES (NULL, 'zoe')",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preview SQL missing %q:\n%s", want, out)
		}
	}
}

// --- Query parameters ------------------------------------------------------

func TestExtractParams(t *testing.T) {
	got := extractParams("SELECT * FROM t WHERE a = :x AND b = ${y} AND c = ?")
	want := []string{"x", "y", "?1"}
	if len(got) != len(want) {
		t.Fatalf("extractParams = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("extractParams[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractParamsIgnoresQuoted(t *testing.T) {
	got := extractParams("SELECT ':notaparam' WHERE x = :real")
	if len(got) != 1 || got[0] != "real" {
		t.Errorf("extractParams = %v, want [real]", got)
	}
}

func TestSubstituteParams(t *testing.T) {
	sql := "SELECT * FROM t WHERE a = :x AND b = ${y} AND c = ? AND d = ':x'"
	out := substituteParams(sql, map[string]string{"x": "1", "y": "2", "?1": "3"})
	for _, want := range []string{"a = 1", "b = 2", "c = 3", "d = ':x'"} {
		if !strings.Contains(out, want) {
			t.Errorf("substituteParams missing %q:\n%s", want, out)
		}
	}
}

func TestAutoLimit(t *testing.T) {
	if got := autoLimit("SELECT * FROM t", "mysql", 100); got != "SELECT * FROM t\nLIMIT 100" {
		t.Errorf("autoLimit = %q", got)
	}
	if got := autoLimit("SELECT * FROM t LIMIT 5", "mysql", 100); got != "SELECT * FROM t LIMIT 5" {
		t.Errorf("existing limit must be kept, got %q", got)
	}
	if got := autoLimit("SELECT TOP 5 * FROM t", "mysql", 100); got != "SELECT TOP 5 * FROM t" {
		t.Errorf("TOP statements must be left alone, got %q", got)
	}
	if got := autoLimit("SELECT * FROM t", "mysql", 0); got != "SELECT * FROM t" {
		t.Errorf("0 means no limit, got %q", got)
	}
}

func TestResultName(t *testing.T) {
	got := resultName("-- name: Recent orders\nSELECT * FROM orders")
	if got != "Recent orders" {
		t.Errorf("resultName = %q", got)
	}
	if got := resultName("SELECT 1"); got != "" {
		t.Errorf("expected empty name, got %q", got)
	}
}

// --- quick-open anchored matching ------------------------------------------

func TestAnchoredScore(t *testing.T) {
	if ok, _ := anchoredScore("us", "users", true, false); !ok {
		t.Error("prefix ^us must match users")
	}
	if ok, _ := anchoredScore("users", "us", true, false); ok {
		t.Error("prefix must not match shorter text")
	}
	if ok, _ := anchoredScore("rs", "users", false, true); !ok {
		t.Error("suffix $rs must match users")
	}
	if ok, s := anchoredScore("users", "users", true, true); !ok || s <= 4 {
		t.Errorf("exact ^users$ should score highest, got ok=%v s=%d", ok, s)
	}
	if ok, _ := anchoredScore("xyz", "users", true, false); ok {
		t.Error("non-matching prefix must not match")
	}
}

func TestQuickOpenAnchoredSyntax(t *testing.T) {
	ws := &workspace{side: newSidebar(func(string) {}, func() {})}
	ws.side.appendHistory("SELECT * FROM users")

	// Exact anchored history SQL.
	got := ws.quickOpenSearch("^select * from users$")
	if len(got) != 1 || got[0].SQL != "SELECT * FROM users" {
		t.Errorf("^…$ exact search returned %d hits: %+v", len(got), got)
	}
	// An anchored pattern matching nothing returns nothing.
	if got := ws.quickOpenSearch("^zzz$"); len(got) != 0 {
		t.Errorf("^zzz$ expected 0 hits, got %d", len(got))
	}
}

func TestQuickOpenActionsOnEmptyPattern(t *testing.T) {
	ws := &workspace{side: newSidebar(func(string) {}, func() {})}
	ws.side.appendHistory("SELECT 1")
	actions := 0
	for _, m := range ws.quickOpenSearch("") {
		if m.Kind == "action" {
			actions++
		}
	}
	if actions == 0 {
		t.Error("empty pattern should surface application actions")
	}
	// A real search term hides the action list.
	actions = 0
	for _, m := range ws.quickOpenSearch("select") {
		if m.Kind == "action" {
			actions++
		}
	}
	if actions != 0 {
		t.Errorf("actions must not match real search terms, got %d", actions)
	}
}

// --- theme JSON round-trip ---------------------------------------------------

func TestThemeJSONRoundTrip(t *testing.T) {
	src := &lightPalette
	m := paletteToMap(src)
	if len(m) == 0 {
		t.Fatal("paletteToMap returned no colors")
	}
	dst := palette{} // zero palette
	if n := applyPaletteMap(m, &dst); n == 0 {
		t.Fatal("applyPaletteMap applied nothing")
	}
	if dst.accent != src.accent || dst.ribbonBG != src.ribbonBG {
		t.Errorf("round-trip mismatch: accent=%v ribbonBG=%v vs %v %v",
			dst.accent, dst.ribbonBG, src.accent, src.ribbonBG)
	}
}

// --- privacy ----------------------------------------------------------------

func TestMaskedPart(t *testing.T) {
	prev := privacyMode
	defer func() { privacyMode = prev }()

	privacyMode = true
	if got := maskedPart("db.example.com"); got != "•••" {
		t.Errorf("maskedPart = %q", got)
	}
	if got := maskedPart(""); got != "" {
		t.Errorf("empty value must stay empty, got %q", got)
	}
	privacyMode = false
	if got := maskedPart("db.example.com"); got != "db.example.com" {
		t.Errorf("privacy off must not mask, got %q", got)
	}
}

// TestColumnWidthFreeze locks the manual column-resize model: once frozen the
// grid reports the stored absolute widths in both totalMinWidth (scroll range)
// and columnWidths (ignoring the available width), and hidden columns drop out
// of the width sum.
func TestColumnWidthFreeze(t *testing.T) {
	g := newTestGrid(3, false)
	auto := g.columnWidths(400)
	if len(auto) != 2 || auto[0] <= 0 {
		t.Fatalf("auto widths: %v", auto)
	}
	g.frozen = true
	g.colWidths = []float32{120, 80}
	if got := g.totalMinWidth(); got != gutterWidth+200 {
		t.Fatalf("frozen totalMinWidth = %v, want %v", got, gutterWidth+200)
	}
	got := g.columnWidths(500)
	if got[0] != 120 || got[1] != 80 {
		t.Fatalf("frozen columnWidths = %v, want [120 80]", got)
	}
	g.hidden = map[int]bool{1: true}
	if got := g.totalMinWidth(); got != gutterWidth+120 {
		t.Fatalf("with hidden col2 totalMinWidth = %v, want %v", got, gutterWidth+120)
	}
}
