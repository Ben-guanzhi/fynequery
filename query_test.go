package main

import (
	"strings"
	"testing"

	"github.com/dbcore/dbcore/sqltext"

	"fynequery/db"
)

// TestQueryPaneUsesLiveSession guards against the pane holding the session it
// was built with. The workspace closes that session when the user switches
// databases, so Run must consult the live session instead (otherwise the
// plugin answers "unknown connection ... (closed?)").
func TestQueryPaneUsesLiveSession(t *testing.T) {
	stale := &fakeSession{}
	live := &fakeSession{}

	ws := &workspace{sess: stale}
	qp := &queryPane{sess: stale, sessFn: func() db.Session { return ws.sess }}

	if got := qp.session(); got != db.Session(stale) {
		t.Fatalf("before switch: got %v want stale", got)
	}
	ws.sess = live // switchDatabase closes stale and installs a new session
	if got := qp.session(); got != db.Session(live) {
		t.Fatalf("after switch: got %v want live", got)
	}
}

func TestQueryPaneSessionFallsBackToField(t *testing.T) {
	sess := &fakeSession{}
	qp := &queryPane{sess: sess}
	if got := qp.session(); got != db.Session(sess) {
		t.Fatalf("got %v want sess", got)
	}
}

// TestShowQueryRowFillsDetails checks that selecting a row in a SQL result
// grid populates the right 详情 pane, read-only (query results have no key to
// write back through).
func TestShowQueryRowFillsDetails(t *testing.T) {
	ws := &workspace{details: newDetailsPane()}
	data := &TableData{
		Cols: []ColumnInfo{{Name: "id", Type: "bigint"}, {Name: "name", Type: "varchar"}},
		Rows: [][]*string{{str("1"), str("pump")}},
	}

	ws.showQueryRow(data, 0)
	if len(ws.details.fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(ws.details.fields))
	}
	if got := ws.details.fields[0]; got.Name != "id" || !got.ReadOnly {
		t.Fatalf("field[0] = %+v, want id read-only", got)
	}
	if v := ws.details.fields[1].Value; v == nil || *v != "pump" {
		t.Fatalf("field[1] value = %v, want pump", v)
	}

	// Out-of-range clears back to the prompt.
	ws.showQueryRow(data, 9)
	if len(ws.details.fields) != 0 {
		t.Fatalf("out-of-range should clear details, got %d fields", len(ws.details.fields))
	}
}

// TestQuoteTableMatchesSqltext pins the table quoter to the shared dialect
// layer. It used to hand-roll MySQL backticks and ANSI for everything else,
// which silently missed SQL Server's brackets.
func TestQuoteTableMatchesSqltext(t *testing.T) {
	drivers := []string{"mysql", "mariadb", "postgres", "postgresql", "sqlserver", "mssql", "oracle", "sqlite", "nope"}
	names := []string{"t", "a`b", `a"b`, "a]b", strings.Repeat("x", 300), "a\x00b"}
	for _, driver := range drivers {
		for _, name := range names {
			ws := &workspace{conn: Connection{Driver: driver}}
			if got, want := ws.quoteTable(name), sqltext.QuoteIdentLoose(driver, name); got != want {
				t.Errorf("quoteTable(%q, %q) = %q, want %q", name, driver, got, want)
			}
		}
	}
}

// TestTruncationNote checks the status-line decoration a capped result gets.
// The flag used to be dropped on the floor: the first page was shown as if it
// were the whole result, which is exactly what sqlkit.ReadRows now reports and
// what this note is for.
func TestTruncationNote(t *testing.T) {
	if got := truncationNote("42 rows", false); got != "42 rows" {
		t.Errorf("truncationNote(false) = %q, want unchanged", got)
	}
	if got := truncationNote("42 rows", true); got != "42 rows · truncated" {
		t.Errorf("truncationNote(true) = %q, want a truncation note", got)
	}
}

// --- command-history navigation (Ctrl+Up / Ctrl+Down) -----------------------

func historyTestPane(t *testing.T, entries ...string) *queryPane {
	t.Helper()
	// 隔离 config 目录: newSidebar 会从磁盘载入持久化历史,不隔离的话真实
	// (或其他测试异步写入的)历史会混进本测试的导航断言。
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	qp := newTestQueryPane()
	qp.side = newSidebar(nil, nil)
	for _, e := range entries {
		qp.side.appendHistory(e, "")
	}
	return qp
}

// TestCommandHistoryNavigation 钉住命令历史导航: Ctrl+Up 从最新往回翻、到最旧
// 停住;Ctrl+Down 往前翻、越过最新一条后恢复导航前的草稿。
func TestCommandHistoryNavigation(t *testing.T) {
	qp := historyTestPane(t, "SELECT 1", "SELECT 2", "SELECT 3")

	qp.historyPrev()
	if got := qp.editor.Text; got != "SELECT 3" {
		t.Fatalf("first prev = %q, want SELECT 3 (newest)", got)
	}
	qp.historyPrev()
	if got := qp.editor.Text; got != "SELECT 2" {
		t.Fatalf("second prev = %q, want SELECT 2", got)
	}
	// 到最旧: 再按 prev 停住不越界。
	qp.historyPrev()
	if got := qp.editor.Text; got != "SELECT 1" {
		t.Fatalf("at oldest, prev = %q, want SELECT 1", got)
	}
	qp.historyPrev()
	if got := qp.editor.Text; got != "SELECT 1" {
		t.Fatalf("prev past oldest = %q, want SELECT 1", got)
	}

	// Down 往前翻: SELECT 2 → SELECT 3 → 恢复草稿。
	qp.historyNext()
	if got := qp.editor.Text; got != "SELECT 2" {
		t.Fatalf("next = %q, want SELECT 2", got)
	}
	qp.historyNext()
	if got := qp.editor.Text; got != "SELECT 3" {
		t.Fatalf("next = %q, want SELECT 3 (newest)", got)
	}
	qp.historyNext()
	if got := qp.editor.Text; got != "" {
		t.Fatalf("next past newest = %q, want the live draft restored (empty)", got)
	}
	qp.historyNext()
	if got := qp.editor.Text; got != "" {
		t.Fatalf("next at live = %q, want empty", got)
	}
}

// TestCommandHistoryDraftRestored 导航前正在输入的草稿必须被保存: 翻回去再翻
// 回来,草稿原样恢复(不会丢掉用户打到一半的语句)。
func TestCommandHistoryDraftRestored(t *testing.T) {
	qp := historyTestPane(t, "SELECT 1")
	qp.editor.SetText("SELECT draft")
	qp.historyPrev()
	if got := qp.editor.Text; got != "SELECT 1" {
		t.Fatalf("prev = %q, want SELECT 1", got)
	}
	qp.historyNext()
	if got := qp.editor.Text; got != "SELECT draft" {
		t.Fatalf("back to live = %q, want the draft restored", got)
	}
}

// TestCommandHistoryEmptyNoop 空历史: 历史导航必须是 no-op,不改动编辑器。
func TestCommandHistoryEmptyNoop(t *testing.T) {
	qp := historyTestPane(t)
	qp.editor.SetText("SELECT draft")
	qp.historyPrev()
	qp.historyNext()
	if got := qp.editor.Text; got != "SELECT draft" {
		t.Fatalf("empty history must be a no-op, got %q", got)
	}
}
