package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
)

// newTestWorkspace builds a workspace on a real test window + canvas so the
// tab panes can be shown/hidden and rendered without the full openWorkspace
// flow (no real DB session needed for the structural checks).
func newTestWorkspace() *workspace {
	a := test.NewApp()
	w := a.NewWindow("t")
	ws := &workspace{window: w, sess: &fakeSession{}}
	ws.tabs = newTabBar(func(i int) { ws.tabs.setActive(i); ws.showTab(i) }, func() { ws.addQueryTab() })
	ws.tabs.onCloseTab = ws.closeTab
	ws.tabs.current = 0
	ws.dataStack = container.NewStack()
	ws.content = container.NewStack(ws.dataStack)
	w.SetContent(ws.content)
	return ws
}

// ensureEditor materialises a pane's editor so per-tab text can be inspected
// without depending on async rendering.
func ensureEditor(qp *queryPane) {
	if qp.editor == nil {
		qp.CreateRenderer()
	}
}

// TestQueryTabs checks the multi-tab editor: each tab owns an independent
// pane/editor, switching re-points the active pane, and closing falls back to
// a survivor while the data tab stays uncloseable.
func TestQueryTabs(t *testing.T) {
	ws := newTestWorkspace()
	if len(ws.queryTabs) != 0 {
		t.Fatalf("initial query tabs = %d, want 0", len(ws.queryTabs))
	}

	ws.addQueryTab()
	if len(ws.queryTabs) != 1 || ws.tabs.current != 0 {
		t.Fatalf("after add: queryTabs=%d current=%d, want 1/0", len(ws.queryTabs), ws.tabs.current)
	}
	active := ws.activeQueryPane()
	if active == nil || ws.queryTabs[0] != active {
		t.Fatal("active pane is not the added one")
	}

	ensureEditor(active)
	active.editor.SetText("SELECT 1")

	// A second tab starts empty and becomes the active editor.
	ws.addQueryTab()
	if len(ws.queryTabs) != 2 || ws.tabs.current != 1 {
		t.Fatalf("after 2nd add: queryTabs=%d current=%d, want 2/1", len(ws.queryTabs), ws.tabs.current)
	}
	p0, p1 := ws.queryTabs[0], ws.queryTabs[1]
	if p0 == p1 {
		t.Fatal("query panes are not distinct instances")
	}
	ensureEditor(p0)
	ensureEditor(p1)
	if p0.editor == p1.editor {
		t.Fatal("two tabs share one editor widget — not independent")
	}
	p1.editor.SetText("SELECT 2")
	if p0.editor.Text != "SELECT 1" {
		t.Fatalf("tab 0 text changed to %q", p0.editor.Text)
	}

	// Switching back re-points the active pane and keeps both texts.
	ws.tabs.setActive(0)
	ws.showTab(0)
	if ws.activeQueryPane() != p0 {
		t.Fatal("switching back did not restore the first pane")
	}
	if p0.editor.Text != "SELECT 1" || p1.editor.Text != "SELECT 2" {
		t.Fatalf("texts lost on switch: %q / %q", p0.editor.Text, p1.editor.Text)
	}
}

// TestCloseQueryTab drives the close handler: closing a query tab removes
// exactly that pane and leaves the data stack plus the survivors in order.
func TestCloseQueryTab(t *testing.T) {
	ws := newTestWorkspace()
	ws.addQueryTab()
	ws.addQueryTab()
	if len(ws.queryTabs) != 2 {
		t.Fatalf("want 2 query tabs, got %d", len(ws.queryTabs))
	}

	// Closing the active (2nd, index 1) query tab falls back to the survivor.
	ws.tabs.onCloseTab(1)
	if len(ws.queryTabs) != 1 {
		t.Fatalf("after close: %d query tabs, want 1", len(ws.queryTabs))
	}
	if len(ws.content.Objects) != 2 {
		t.Fatalf("content objects = %d, want dataStack + 1 pane", len(ws.content.Objects))
	}
	if ws.content.Objects[0] != ws.dataStack {
		t.Fatal("data stack must stay first in the content stack")
	}
	if ws.activeQueryPane() != ws.queryTabs[0] {
		t.Fatal("after close the survivor should be active")
	}
}

// TestQueryTabIndex pins the strip-index mapping used by the quick-open
// palette to focus a query tab (data docs shift query panes off 0).
func TestQueryTabIndex(t *testing.T) {
	ws := &workspace{}
	q0, q1, q2 := &queryPane{}, &queryPane{}, &queryPane{}
	ws.docs = []*docTab{
		{kind: docData, data: &dataTableTab{name: "t"}},
		{kind: docQuery, qp: q0},
		{kind: docQuery, qp: q1},
		{kind: docQuery, qp: q2},
	}
	if i := ws.queryTabIndex(q0); i != 1 {
		t.Fatalf("idx0 = %d, want 1", i)
	}
	if i := ws.queryTabIndex(q2); i != 3 {
		t.Fatalf("idx2 = %d, want 3", i)
	}
	if i := ws.queryTabIndex(&queryPane{}); i != -1 {
		t.Fatalf("unknown pane idx = %d, want -1", i)
	}
}

// TestActiveQueryPane pins that the active tab's pane is returned, falling
// back to the last pane when none is active.
func TestActiveQueryPane(t *testing.T) {
	q0, q1 := &queryPane{}, &queryPane{}
	ws := &workspace{
		tabs:      &tabBar{},
		queryTabs: []*queryPane{q0, q1},
	}
	if got := ws.activeQueryPane(); got != q1 {
		t.Fatalf("no docs -> want last pane, got %v", got)
	}
	ws.docs = []*docTab{{kind: docQuery, qp: q0}, {kind: docQuery, qp: q1}}
	ws.tabs.current = 0
	if got := ws.activeQueryPane(); got != q0 {
		t.Fatalf("current=0 -> want pane 0, got %v", got)
	}
}

// newTabHarness wires just enough of build()'s single-instance chrome for
// applyDataTab/showTab/closeTab to run without the async DB loads.
func newTabHarness() *workspace {
	a := test.NewApp()
	w := a.NewWindow("t")
	ws := &workspace{window: w, sess: &fakeSession{}, conn: Connection{Driver: "mysql"}}
	ws.tabs = newTabBar(func(i int) { ws.tabs.setActive(i); ws.showTab(i) }, func() { ws.addQueryTab() })
	ws.tabs.onCloseTab = ws.closeTab
	ws.status = newStatusBar()
	ws.grid = newDataTable(emptyTableData(), ws.status)
	ws.dataSlot = container.NewStack(ws.grid)
	ws.structPane = container.NewStack()
	ws.indexPane = container.NewStack()
	ws.dataStack = container.NewStack(ws.dataSlot, ws.structPane, ws.indexPane)
	ws.content = container.NewStack(ws.dataStack)
	ws.segment = newSegmented([]string{"Data", "Structure", "Indexes"}, func(int) {})
	ws.filterBtn = newFlatButton("Filter")
	ws.details = newDetailsPane()
	ws.bottomBar = container.NewVBox()
	ws.body = container.NewStack(ws.content)
	w.SetContent(ws.body)
	return ws
}

// TestMultiTableTabs pins multi-table browsing: each open table is its own
// closable tab holding an independent grid, switching swaps/restores grids,
// and closing leaves a survivor active.
func TestMultiTableTabs(t *testing.T) {
	ws := newTabHarness()

	t1 := &dataTableTab{name: "users"}
	t2 := &dataTableTab{name: "orders"}
	t1.grid = newDataTable(emptyTableData(), ws.status)
	t2.grid = newDataTable(emptyTableData(), ws.status)
	ws.dataTabs = append(ws.dataTabs, t1, t2)
	i1 := ws.addDoc(docData, "users", true, t1, nil)
	i2 := ws.addDoc(docData, "orders", true, t2, nil)

	ws.tabs.setActive(i1)
	ws.showTab(i1)
	if ws.tableName != "users" || ws.activeData != t1 {
		t.Fatalf("tab1: name=%q active=%v", ws.tableName, ws.activeData == t1)
	}
	g1 := ws.grid

	ws.tabs.setActive(i2)
	ws.showTab(i2)
	if ws.tableName != "orders" || ws.activeData != t2 {
		t.Fatalf("tab2: name=%q active=%v", ws.tableName, ws.activeData == t2)
	}
	if ws.grid == g1 {
		t.Fatal("switching tabs did not swap the grid")
	}
	if t1.grid == nil || t1.grid == ws.grid {
		t.Fatal("outgoing tab did not keep its own grid")
	}

	ws.tabs.setActive(i1)
	ws.showTab(i1)
	if ws.grid != g1 {
		t.Fatal("switching back did not restore the first grid")
	}

	ws.closeTab(i2)
	if len(ws.dataTabs) != 1 || ws.dataTabs[0] != t1 {
		t.Fatalf("after close: dataTabs=%d", len(ws.dataTabs))
	}
	if ws.tableName != "users" || ws.activeData != t1 {
		t.Fatalf("after close: name=%q", ws.tableName)
	}
}

// keep fyne imported for the helper types even if a build trims it.
var _ = fyne.CurrentApp

// TestTabStripVisibleWithSingleDoc pins that the document tab strip appears as
// soon as one document is open (so the open table/query name is visible in the
// content area) and hides again when no documents remain (clean blank state).
func TestTabStripVisibleWithSingleDoc(t *testing.T) {
	ws := newTestWorkspace()
	ws.tabStrip = container.NewStack()

	ws.syncTabStrip()
	if ws.tabStrip.Visible() {
		t.Fatal("tab strip should be hidden with no documents")
	}

	ws.addQueryTab() // one document open
	if len(ws.tabs.tabs) != 1 {
		t.Fatalf("expected 1 tab, got %d", len(ws.tabs.tabs))
	}
	if !ws.tabStrip.Visible() {
		t.Fatal("tab strip should be visible once a document is open")
	}

	// Closing the last document hides the strip again.
	ws.closeTab(ws.tabs.current)
	if ws.tabStrip.Visible() {
		t.Fatal("tab strip should hide after the last document closes")
	}
}
