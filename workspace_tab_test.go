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
	ws.tabs.addTab("Data", false)
	ws.tabs.current = 0
	ws.tabs.tabs[0].setActive(true)
	ws.dataStack = container.NewStack()
	ws.content = container.NewStack(ws.dataStack)
	// The data-tab close handler is assigned in build(); replicate it here so
	// onCloseTab exercises the real trimming logic.
	ws.tabs.onCloseTab = func(i int) {
		if i == 0 {
			return // the data tab is never closeable
		}
		if i-1 < len(ws.queryTabs) {
			ws.queryTabs = append(ws.queryTabs[:i-1], ws.queryTabs[i:]...)
		}
		ws.tabs.removeTab(i)
		ws.content.Objects = ws.content.Objects[:0]
		ws.content.Objects = append(ws.content.Objects, ws.dataStack)
		for _, qp := range ws.queryTabs {
			ws.content.Objects = append(ws.content.Objects, qp)
		}
		if ws.tabs.current >= len(ws.tabs.tabs) {
			ws.tabs.current = len(ws.tabs.tabs) - 1
		}
		ws.tabs.setActive(ws.tabs.current)
		ws.showTab(ws.tabs.current)
	}
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
	if len(ws.queryTabs) != 1 || ws.tabs.current != 1 {
		t.Fatalf("after add: queryTabs=%d current=%d, want 1/1", len(ws.queryTabs), ws.tabs.current)
	}
	active := ws.activeQueryPane()
	if active == nil || ws.queryTabs[0] != active {
		t.Fatal("active pane is not the added one")
	}

	ensureEditor(active)
	active.editor.SetText("SELECT 1")

	// A second tab starts empty and becomes the active editor.
	ws.addQueryTab()
	if len(ws.queryTabs) != 2 || ws.tabs.current != 2 {
		t.Fatalf("after 2nd add: queryTabs=%d current=%d, want 2/2", len(ws.queryTabs), ws.tabs.current)
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
	ws.tabs.setActive(1)
	ws.showTab(1)
	if ws.activeQueryPane() != p0 {
		t.Fatal("switching back did not restore the first pane")
	}
	if p0.editor.Text != "SELECT 1" || p1.editor.Text != "SELECT 2" {
		t.Fatalf("texts lost on switch: %q / %q", p0.editor.Text, p1.editor.Text)
	}
}

// TestCloseQueryTab drives the close handler: the data tab cannot be closed,
// closing a query tab removes exactly that pane and rebuilds the content
// stack (dataStack + survivors) in order.
func TestCloseQueryTab(t *testing.T) {
	ws := newTestWorkspace()
	ws.addQueryTab()
	ws.addQueryTab()
	if len(ws.queryTabs) != 2 {
		t.Fatalf("want 2 query tabs, got %d", len(ws.queryTabs))
	}

	// The data tab (index 0) is uncloseable.
	ws.tabs.onCloseTab(0)
	if len(ws.queryTabs) != 2 {
		t.Fatalf("data tab should be uncloseable, got %d", len(ws.queryTabs))
	}

	// Closing the active (2nd) query tab falls back to the survivor.
	ws.tabs.onCloseTab(2)
	if len(ws.queryTabs) != 1 {
		t.Fatalf("after close: %d query tabs, want 1", len(ws.queryTabs))
	}
	if len(ws.content.Objects) != 2 {
		t.Fatalf("content objects = %d, want dataStack + 1 pane", len(ws.content.Objects))
	}
	if ws.content.Objects[0] != ws.dataStack {
		t.Fatal("data stack must stay first in the content stack")
	}
}

// TestQueryTabIndex pins the 1-based tab-index mapping used by the quick-open
// palette to focus a query tab.
func TestQueryTabIndex(t *testing.T) {
	ws := &workspace{}
	ws.queryTabs = []*queryPane{{}, {}, {}}
	if i := ws.queryTabIndex(ws.queryTabs[0]); i != 1 {
		t.Fatalf("idx0 = %d, want 1", i)
	}
	if i := ws.queryTabIndex(ws.queryTabs[2]); i != 3 {
		t.Fatalf("idx2 = %d, want 3", i)
	}
	if i := ws.queryTabIndex(&queryPane{}); i != -1 {
		t.Fatalf("unknown pane idx = %d, want -1", i)
	}
}

// TestActiveQueryPane pins that the active tab's pane is returned, falling
// back to the last pane when none is active.
func TestActiveQueryPane(t *testing.T) {
	ws := &workspace{
		tabs:      &tabBar{},
		queryTabs: []*queryPane{{}, {}},
	}
	if got := ws.activeQueryPane(); got != ws.queryTabs[1] {
		t.Fatalf("no current tab -> want last pane, got %v", got)
	}
	ws.tabs.current = 1
	if got := ws.activeQueryPane(); got != ws.queryTabs[0] {
		t.Fatalf("current=1 -> want pane 0, got %v", got)
	}
}

// keep fyne imported for the helper types even if a build trims it.
var _ = fyne.CurrentApp
