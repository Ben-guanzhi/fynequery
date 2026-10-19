package main

// 面包屑栏回归（P1 移植自内置版 workspace.go:880 buildCrumbBar）。
// 钉住三件事：无表时显示占位符、表名取自 grid.src、< > 跟随分页状态启停。

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// newCrumbClient lays the real query view out on the test canvas so the crumb
// widgets exist and have been through a layout pass.
func newCrumbClient(t *testing.T) *Client {
	t.Helper()
	a := test.NewApp()
	w := a.NewWindow("t")
	c := &Client{
		app:   a,
		win:   w,
		store: db.NewStore(),
		tree:  treeState{nodes: make(map[string]*nodeData), expanded: make(map[string]bool)},
		cache: memoCache{tables: make(map[string][]string), objects: make(map[string][]string), loading: make(map[string]bool), loadingObjects: make(map[string]bool), fks: make(map[string][]db.ForeignKeyInfo)},
		grid:  gridState{edits: newGridEditState(), sortCol: -1},
	}
	w.SetContent(c.buildQueryView())
	w.Resize(fyne.NewSize(1000, 700))
	return c
}

func TestCrumbBarBuiltWithPlaceholder(t *testing.T) {
	c := newCrumbClient(t)
	if c.crumb.name == nil {
		t.Fatal("面包屑表名控件未建")
	}
	if got := c.crumb.name.Text; got != "—" {
		t.Fatalf("初始表名 = %q, want %q", got, "—")
	}
	if c.crumb.bar == nil {
		t.Fatal("面包屑容器未建")
	}
	// 未翻页时两个步进按钮都该是禁用的（stepPage 本身也会 no-op）。
	if !c.crumb.prev.Disabled() || !c.crumb.next.Disabled() {
		t.Fatal("无可翻页结果时 < > 应禁用")
	}
}

// TestCrumbBarShowsTableNameFromGridSrc pins that the name comes from the
// standalone's db.TableRef, not from the MySQL-only information_schema query
// the embedded copy used — so all five engines get a name.
func TestCrumbBarShowsTableNameFromGridSrc(t *testing.T) {
	c := newCrumbClient(t)
	c.grid.src = db.TableRef{ConnID: "c1", Database: "shop", Table: "orders"}
	c.refreshCrumb()
	if got := c.crumb.name.Text; got != "orders" {
		t.Fatalf("表名 = %q, want %q", got, "orders")
	}
}

// TestCrumbSteppersFollowPagedResult pins the < > buttons to the pager state:
// they only enable once the on-screen result is a pageable one.
func TestCrumbSteppersFollowPagedResult(t *testing.T) {
	c := newCrumbClient(t)
	c.page.stmt = "SELECT * FROM orders"
	c.page.no = 1
	c.page.size = 300
	c.refreshCrumb()
	if c.crumb.prev.Disabled() || c.crumb.next.Disabled() {
		t.Fatal("可翻页结果下 < > 应启用")
	}
	// 结果换成不可翻页的（比如多语句/非 SELECT）后应重新禁用。
	c.page.stmt = ""
	c.refreshCrumb()
	if !c.crumb.prev.Disabled() || !c.crumb.next.Disabled() {
		t.Fatal("不可翻页结果下 < > 应禁用")
	}
}
