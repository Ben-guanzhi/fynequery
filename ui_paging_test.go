package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// These tests drive the paging bar against a real SQLite file: the row window
// is rendered by the SQLite dialect and re-queried on every page turn, so the
// only faithful check is counting rows that actually came back.

// newPagingClient seeds items with n rows and connects a client to the file,
// building the whole UI. paging switches 分页浏览 on through the real toolbar
// checkbox, and size drives 每页 through the bar's own selector, so both tests
// exercise the widgets rather than just the state behind them.
func newPagingClient(t *testing.T, n int, paging bool, size string) *Client {
	t.Helper()
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)

	dir := t.TempDir()
	dbFile := filepath.Join(dir, "t.db")
	sdb, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sdb.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A keyless twin so the unstable-order path has something to warn about.
	if _, err := sdb.Exec(`CREATE TABLE nokey (name TEXT)`); err != nil {
		t.Fatalf("create nokey: %v", err)
	}
	// A four-column composite key, to check every column reaches the SQL.
	if _, err := sdb.Exec(`CREATE TABLE compkey (a INTEGER, b INTEGER, c INTEGER, d INTEGER, name TEXT, ` +
		`PRIMARY KEY (a, b, c, d))`); err != nil {
		t.Fatalf("create compkey: %v", err)
	}
	for i := 1; i <= n; i++ {
		if _, err := sdb.Exec(`INSERT INTO items (name) VALUES (?)`, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		if _, err := sdb.Exec(`INSERT INTO nokey (name) VALUES (?)`, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatalf("seed nokey %d: %v", i, err)
		}
		if _, err := sdb.Exec(`INSERT INTO compkey (a, b, c, d, name) VALUES (?, 1, 1, 1, ?)`,
			i, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatalf("seed compkey %d: %v", i, err)
		}
	}
	sdb.Close()

	if err := c.connect(db.ConnConfig{ID: "c0", Name: "t", Type: db.DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })

	w.SetContent(c.buildUI())
	c.pageCheck.SetChecked(paging)
	c.pageSizeSel.SetSelected(size)
	c.store.ReplaceHistory(nil) // count only what this test records
	return c
}

// TestPagingFetchesOneWindowPerPage pins the whole loop: the first run fetches
// only 每页 rows, 下一页 re-queries the next window, and the last (short) page
// disables it. Page turns must not add history entries.
func TestPagingFetchesOneWindowPerPage(t *testing.T) {
	c := newPagingClient(t, 25, true, "10")
	c.queryEntry.setTextSilently("SELECT id, name FROM items ORDER BY id")

	c.runCurrent()
	if len(c.gridRows) != 10 {
		t.Fatalf("page 1 rows = %d, want 10", len(c.gridRows))
	}
	if c.gridRows[0][1] != "row-1" {
		t.Fatalf("page 1 starts at %q", c.gridRows[0][1])
	}
	if c.pageStmt == "" || c.pageNo != 1 {
		t.Fatalf("paging state = %q page %d, want page 1 of a statement", c.pageStmt, c.pageNo)
	}
	if !c.pageBar.Visible() {
		t.Fatal("page bar hidden while a paged result is on screen")
	}
	if got := c.pageLabel.Text; got != "第 1 页 · 本页 10 行" {
		t.Fatalf("page label = %q", got)
	}
	if !c.prevPageBtn.Disabled() {
		t.Fatal("上一页 enabled on page 1")
	}
	if c.nextPageBtn.Disabled() {
		t.Fatal("下一页 disabled on a full page")
	}

	c.stepPage(1)
	if c.pageNo != 2 || len(c.gridRows) != 10 || c.gridRows[0][1] != "row-11" {
		t.Fatalf("page 2 = page %d, %d rows starting %q", c.pageNo, len(c.gridRows), c.gridRows[0][1])
	}
	if c.prevPageBtn.Disabled() {
		t.Fatal("上一页 disabled on page 2")
	}

	c.stepPage(1)
	if c.pageNo != 3 || len(c.gridRows) != 5 {
		t.Fatalf("last page = page %d, %d rows", c.pageNo, len(c.gridRows))
	}
	if !c.nextPageBtn.Disabled() {
		t.Fatal("下一页 still enabled after a short page")
	}

	if h := c.store.History(); len(h) != 1 {
		t.Fatalf("history entries = %d, want 1 (page turns are not new statements)", len(h))
	}
}

// TestPagingBarHiddenForOwnWindowStatement checks the guard the dialect shares
// with 行数上限: SQL that already caps its own rows is run as written, and the
// bar stays out of the way with a status line explaining why.
func TestPagingBarHiddenForOwnWindowStatement(t *testing.T) {
	c := newPagingClient(t, 25, true, "10")
	c.queryEntry.setTextSilently("SELECT id, name FROM items ORDER BY id LIMIT 3")

	c.runCurrent()
	if len(c.gridRows) != 3 {
		t.Fatalf("rows = %d, want the statement's own 3", len(c.gridRows))
	}
	if c.pageStmt != "" {
		t.Fatalf("pageStmt = %q, want empty for a self-limited statement", c.pageStmt)
	}
	if c.pageBar.Visible() {
		t.Fatal("page bar shown for a statement that cannot be paged")
	}
	if !strings.Contains(c.status.Text, "未分页") {
		t.Fatalf("status = %q, want an explanation", c.status.Text)
	}
}

// TestPageSizeChangeRefetchesFirstPage checks 每页行数 takes effect immediately
// on a paged result rather than only on the next run.
func TestPageSizeChangeRefetchesFirstPage(t *testing.T) {
	c := newPagingClient(t, 25, true, "10")
	c.queryEntry.setTextSilently("SELECT id, name FROM items ORDER BY id")
	c.runCurrent()
	c.stepPage(1)
	if c.pageNo != 2 {
		t.Fatalf("page = %d, want 2 before the size change", c.pageNo)
	}

	c.pageSizeSel.SetSelected("50")
	if c.pageSize != 50 || c.pageNo != 1 {
		t.Fatalf("after size change: size %d page %d", c.pageSize, c.pageNo)
	}
	if len(c.gridRows) != 25 || c.gridRows[0][1] != "row-1" {
		t.Fatalf("rows = %d starting %q, want all 25 from row-1", len(c.gridRows), c.gridRows[0][1])
	}
	if c.uiSet.PageSize != 50 {
		t.Fatalf("persisted page size = %d, want 50", c.uiSet.PageSize)
	}
	if !c.nextPageBtn.Disabled() {
		t.Fatal("下一页 enabled although the short page holds every row")
	}
}

// TestPagingRefusedWhileChangesPending mirrors the sort guard: an unsaved edit
// queue must not be thrown away by a page turn.
func TestPagingRefusedWhileChangesPending(t *testing.T) {
	c := newPagingClient(t, 25, true, "10")
	c.queryEntry.setTextSilently("SELECT id, name FROM items ORDER BY id")
	c.runCurrent()

	c.edits.setUpdate(0, "name", "edited")
	c.stepPage(1)
	if c.pageNo != 1 {
		t.Fatalf("page turned to %d with unsaved changes", c.pageNo)
	}
	if !strings.Contains(c.status.Text, "未保存") {
		t.Fatalf("status = %q, want the unsaved-changes warning", c.status.Text)
	}
	if !c.edits.any() {
		t.Fatal("the pending edit queue was dropped")
	}
}

// TestPagingInjectsPrimaryKeyOrder is the correctness proof for the weakest
// edge of LIMIT/OFFSET: an unordered statement has no promise about which rows
// a window picks. The pager must sort a single-table SELECT * by its primary key
// and say so on the bar, and the pages must come back disjoint.
func TestPagingInjectsPrimaryKeyOrder(t *testing.T) {
	c := newPagingClient(t, 25, true, "10")
	c.queryEntry.setTextSilently("SELECT * FROM items")

	c.runCurrent()
	// pageStmt holds the statement to page; the window itself is rendered per
	// call by PageSQL (pinned in internal/db/paging_test.go).
	if !strings.HasSuffix(c.pageStmt, `ORDER BY "id"`) {
		t.Fatalf("paged statement = %q, want an injected primary-key sort", c.pageStmt)
	}
	if !strings.Contains(c.pageLabel.Text, "已按主键 id 排序") {
		t.Fatalf("bar = %q, want the injected sort announced", c.pageLabel.Text)
	}
	if c.gridRows[0][1] != "row-1" || c.gridRows[9][1] != "row-10" {
		t.Fatalf("page 1 = %q..%q, want row-1..row-10", c.gridRows[0][1], c.gridRows[9][1])
	}
	c.stepPage(1)
	if c.gridRows[0][1] != "row-11" {
		t.Fatalf("page 2 starts at %q, want row-11", c.gridRows[0][1])
	}
	// The edit context is rebuilt from the rewritten statement, so paging keeps
	// the grid writable exactly where the plain SELECT * was.
	if c.sourceRef.Table != "items" {
		t.Fatalf("source = %+v, want the items table", c.sourceRef)
	}
}

// TestPagingCompositeKeyCountedNotSpelled: a wide key must reach the SQL in
// full, while the bar only counts the columns - spelling out a long key is how
// a one-row bar pushes the turn buttons out of reach.
func TestPagingCompositeKeyCountedNotSpelled(t *testing.T) {
	c := newPagingClient(t, 25, true, "10")
	c.queryEntry.setTextSilently("SELECT * FROM compkey")

	c.runCurrent()
	for _, col := range []string{`"a"`, `"b"`, `"c"`, `"d"`} {
		if !strings.Contains(c.pageStmt, col) {
			t.Fatalf("paged statement = %q, want key column %s", c.pageStmt, col)
		}
	}
	if !strings.Contains(c.pageLabel.Text, "4 列") {
		t.Fatalf("bar = %q, want the key counted", c.pageLabel.Text)
	}
	c.stepPage(1)
	if len(c.gridRows) != 10 || c.gridRows[0][4] != "row-11" {
		t.Fatalf("page 2 = %d rows starting %q", len(c.gridRows), c.gridRows[0][4])
	}
}

// TestPagingWarnsWithoutKey covers the case the pager refuses to guess at: a
// table with no primary key has nothing to sort by, so the bar must warn that
// the windows may overlap rather than invent an order.
func TestPagingWarnsWithoutKey(t *testing.T) {
	c := newPagingClient(t, 25, true, "10")
	c.queryEntry.setTextSilently("SELECT * FROM nokey")

	c.runCurrent()
	if strings.Contains(c.pageStmt, "ORDER BY") {
		t.Fatalf("paged statement = %q, want the user's SQL untouched", c.pageStmt)
	}
	if !strings.Contains(c.pageLabel.Text, "排序不稳定") {
		t.Fatalf("bar = %q, want the instability warning", c.pageLabel.Text)
	}
	if c.pageStmt == "" || !c.pageBar.Visible() {
		t.Fatal("a keyless table must still be browsable page by page")
	}
}

// TestPagingKeepsOwnOrderBy: a statement that already sorts its rows is nobody
// else's business, so the pager adds nothing and says nothing.
func TestPagingKeepsOwnOrderBy(t *testing.T) {
	c := newPagingClient(t, 25, true, "10")
	c.queryEntry.setTextSilently("SELECT id, name FROM items ORDER BY name DESC")

	c.runCurrent()
	if strings.Contains(c.pageStmt, `"id"`) || c.pageNote != "" {
		t.Fatalf("statement = %q note = %q, want the user's sort kept", c.pageStmt, c.pageNote)
	}
	if c.pageStmt != "SELECT id, name FROM items ORDER BY name DESC" {
		t.Fatalf("statement = %q, want it paged exactly as written", c.pageStmt)
	}
	if len(c.gridRows) != 10 {
		t.Fatalf("rows = %d, want one 10-row window", len(c.gridRows))
	}
	if c.gridRows[0][1] != "row-9" {
		t.Fatalf("first row = %q, want the DESC order kept (row-9)", c.gridRows[0][1])
	}
}

// TestPagingCheckGreysRowCap covers the toolbar switch itself: turning paging on
// disables the 行数上限 selector (the page window replaces it) and makes a plain
// SELECT show the bar; unticking takes the bar away again.
func TestPagingCheckGreysRowCap(t *testing.T) {
	c := newPagingClient(t, 5, false, "10")
	if c.limitSel.Disabled() {
		t.Fatal("行数上限 starts disabled")
	}
	c.queryEntry.setTextSilently("SELECT id, name FROM items ORDER BY id")
	c.runCurrent()
	if c.pageBar.Visible() {
		t.Fatal("page bar shown while paging is off")
	}

	test.Tap(c.pageCheck)
	if !c.paging || !c.limitSel.Disabled() {
		t.Fatalf("paging=%v limitSel disabled=%v", c.paging, c.limitSel.Disabled())
	}
	c.runCurrent()
	if !c.pageBar.Visible() || len(c.gridRows) != 5 {
		t.Fatalf("bar visible=%v rows=%d with paging on", c.pageBar.Visible(), len(c.gridRows))
	}

	test.Tap(c.pageCheck)
	if c.paging || c.limitSel.Disabled() {
		t.Fatalf("after unticking: paging=%v limitSel disabled=%v", c.paging, c.limitSel.Disabled())
	}
	if c.pageBar.Visible() {
		t.Fatal("page bar survived turning paging off")
	}
}

// TestFailedQueryKeepsItsError reports what the status line said: the runner
// used to read ctx.Err() after cancelling every query, so a plain failure looked
// like a cancellation. A syntax error must stay a syntax error.
func TestFailedQueryKeepsItsError(t *testing.T) {
	c := newPagingClient(t, 5, false, "10")
	c.queryEntry.setTextSilently("SELECT * FROM missing_table")
	c.runCurrent()
	if !strings.Contains(c.status.Text, "错误") {
		t.Fatalf("status = %q, want the query error", c.status.Text)
	}
	if strings.Contains(c.status.Text, "取消") {
		t.Fatalf("status = %q, a failure must not be reported as cancelled", c.status.Text)
	}
}

// TestPageBarFitsMainPane is the geometry guard: the bar is one row, so the
// longest page label (deep page number plus either note) must still leave the
// turn buttons inside the pane - the failure mode that clipped the toolbar.
func TestPageBarFitsMainPane(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	root := c.buildUI()
	w.SetContent(root)
	w.Resize(fyne.NewSize(1280, 800))

	c.pageStmt = "SELECT * FROM t"
	c.pageNo = 1234
	c.pageSize = 500
	c.gridHeader = []string{"a"}
	c.gridRows = [][]string{{"1"}, {"2"}}
	bar := c.pageBar

	for _, note := range []string{
		" · 已按主键 id, tenant_id, created_at 排序",
		" · ⚠ 排序不稳定，翻页可能重复或漏行",
	} {
		c.pageNote = note
		c.refreshPageBar()
		test.LaidOutObjects(root)
		if !bar.Visible() {
			t.Fatalf("page bar hidden although pageStmt is set (%q)", note)
		}
		if bw := bar.MinSize().Width; bw > bar.Size().Width {
			t.Fatalf("note %q: bar needs %.0f px, the pane gives %.0f px", note, bw, bar.Size().Width)
		}
		children := bar.Objects
		left := children[0]
		right := children[len(children)-1]
		if left.Position().X+left.MinSize().Width > right.Position().X {
			t.Fatalf("note %q: label ends at %.0f px, past the buttons at %.0f px",
				note, left.Position().X+left.MinSize().Width, right.Position().X)
		}
		if right.Position().X+right.MinSize().Width > bar.Size().Width+1 {
			t.Fatalf("note %q: buttons end at %.0f px, past the bar's %.0f px",
				note, right.Position().X+right.MinSize().Width, bar.Size().Width)
		}
		t.Logf("note %q: bar needs %.0f of %.0f px, label %.0f px, buttons %.0f px",
			note, bar.MinSize().Width, bar.Size().Width, left.MinSize().Width, right.MinSize().Width)
	}
}
