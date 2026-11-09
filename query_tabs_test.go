package main

// Run-result tabs: every statement of a Run keeps its grid alive and
// switchable (Run All used to keep only the last statement's result).

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
)

// newTestQueryPane builds a query pane on a real test window with its renderer
// materialised (resultSlot/timer/status/resultBar exist), backed by fakeSession.
func newTestQueryPane() *queryPane {
	a := test.NewApp()
	w := a.NewWindow("q")
	qp := newQueryPane(w, Connection{Driver: "mysql"}, &fakeSession{}, nil)
	qp.CreateRenderer()
	return qp
}

func tabTitles(qp *queryPane) []string {
	out := make([]string, 0, len(qp.resultTabs))
	for _, t := range qp.resultTabs {
		out = append(out, t.title)
	}
	return out
}

func TestResultTabsSwitchAndReset(t *testing.T) {
	qp := newTestQueryPane()
	mkGrid := func() *dataTable {
		g := newDataTable(&TableData{Cols: []ColumnInfo{{Name: "a"}}, Fetched: 0, Total: -1}, qp.status)
		g.window = qp.window
		return g
	}
	g1, g2 := mkGrid(), mkGrid()

	// A single result needs no switcher: the bar stays hidden.
	tab0 := qp.recordResultTab(0, "", "SELECT 1", g1)
	if qp.resultBar.Visible() {
		t.Fatalf("single result must not show the tab bar")
	}
	if len(qp.resultTabs) != 1 || qp.resultTabs[0].title != "stmt 1" {
		t.Fatalf("tabs after first record: %v", tabTitles(qp))
	}

	// The second result reveals the bar and selects the newest tab.
	tab1 := qp.recordResultTab(1, "", "SELECT 2", g2)
	if !qp.resultBar.Visible() {
		t.Fatalf("two results must show the tab bar")
	}
	if qp.resultSel != 1 {
		t.Fatalf("resultSel = %d, want 1 (newest selected)", qp.resultSel)
	}

	// Meta lands on the statement's OWN tab (the pointer recordResultTab
	// returned), never on the currently selected one — the regression: Run
	// All 的慢语句落地后用户点回前一条的 chip,完成回调曾把耗时/状态写进
	// 别人的页签,真正的页签反而空白。顺序刻意镜像真实流程:
	// 记录两条 → 用户切回 tab 0 → 两条依次完成。
	qp.showResultTab(0) // the user switches away mid-run
	if tab0 == nil || tab1 == nil || tab0 == tab1 {
		t.Fatalf("recordResultTab must return per-statement tab pointers")
	}
	tab0.setMeta("5 ms · 2 rows", "● stmt 1 — 1 columns · 2 rows")
	tab1.setMeta("7 ms · 3 rows", "● stmt 2 — 1 columns · 3 rows")
	// tab 0 keeps its own meta even though tab 1 completed last.
	if tab0.timer != "5 ms · 2 rows" || tab0.status == "" {
		t.Fatalf("tab 0 meta overwritten by the later completion: %q / %q", tab0.timer, tab0.status)
	}
	qp.showResultTab(0)
	if qp.resultGrid() != g1 {
		t.Fatalf("showResultTab(0) did not swap to the first tab's grid")
	}
	if qp.timer.Text != "5 ms · 2 rows" {
		t.Fatalf("timer not restored from tab 0 meta: %q", qp.timer.Text)
	}
	qp.showResultTab(1)
	if qp.timer.Text != "7 ms · 3 rows" {
		t.Fatalf("timer not restored from tab 1 meta: %q", qp.timer.Text)
	}

	// A retry of the newest statement's index replaces its tab in place.
	qp.recordResultTab(1, "月度报表", "SELECT 2", mkGrid())
	if got := tabTitles(qp); len(got) != 2 || got[1] != "月度报表" {
		t.Fatalf("retry must replace the tab in place, tabs: %v", got)
	}

	// A fresh run resets the set; the bar hides again.
	qp.resetResultTabs()
	if len(qp.resultTabs) != 0 || qp.resultBar.Visible() || qp.resultSel != -1 {
		t.Fatalf("resetResultTabs left state behind: %d tabs", len(qp.resultTabs))
	}
}

// Run All must keep every statement's result alive: the last result lands in
// the slot, earlier ones stay switchable, and each tab remembers the chrome of
// the statement that produced it.
func TestRunAllKeepsEveryStatementResult(t *testing.T) {
	qp := newTestQueryPane()
	qp.editor.SetText("SELECT * FROM a;\nSELECT * FROM b;")
	qp.runAll()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && qp.running {
		time.Sleep(10 * time.Millisecond)
	}
	if qp.running {
		t.Fatal("runAll did not finish in time")
	}
	if len(qp.resultTabs) != 2 {
		t.Fatalf("result tabs = %d (%v), want 2", len(qp.resultTabs), tabTitles(qp))
	}
	if qp.resultTabs[0].title != "stmt 1" || qp.resultTabs[1].title != "stmt 2" {
		t.Fatalf("tab titles = %v, want [stmt 1 stmt 2]", tabTitles(qp))
	}
	if qp.resultSel != 1 || qp.resultGrid() != qp.resultTabs[1].grid {
		t.Fatalf("the newest result must be selected and shown")
	}
	if len(qp.resultTabs[0].grid.data.Rows) != 2 {
		t.Fatalf("the first statement's grid lost its rows: %d", len(qp.resultTabs[0].grid.data.Rows))
	}
	if qp.resultTabs[0].timer == "" || qp.resultTabs[0].status == "" {
		t.Fatalf("first tab lost its chrome meta: %q / %q", qp.resultTabs[0].timer, qp.resultTabs[0].status)
	}

	// The first statement's result stays switchable after the run.
	first := qp.resultTabs[0].grid
	qp.showResultTab(0)
	if qp.resultGrid() != first {
		t.Fatalf("switching to tab 0 after the run failed")
	}
}
