package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// TestEngineAvatarIsFixedSize pins the avatar's badge size: the canvas shapes
// inside have zero min size, so the fixedSize wrapper must force 26x26 or the
// round badge collapses to nothing in the list.
func TestEngineAvatarIsFixedSize(t *testing.T) {
	av := engineAvatar(db.DbTypeMySQL)
	if got := av.MinSize(); got.Width != 26 || got.Height != 26 {
		t.Errorf("avatar min size = %v, want 26x26", got)
	}
}

// TestConnectionListRowCount pins that the sidebar list shows one row per saved
// connection and, with none saved, the "no connections" placeholder.
func TestConnectionListRowCount(t *testing.T) {
	t.Setenv("TABLEPRO_STATE_DIR", t.TempDir())
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)

	c.store.AddConnection(db.ConnConfig{ID: "c1", Name: "alpha", Type: db.DbTypeMySQL})
	c.store.AddConnection(db.ConnConfig{ID: "c2", Name: "beta", Type: db.DbTypePostgres})
	c.picker.list = container.NewVBox()
	c.refreshConnectionList()
	if got := len(c.picker.list.Objects); got != 2 {
		t.Fatalf("list rows = %d, want 2", got)
	}
}

// TestConnectionListEmptyPlaceholder pins the "no connections" fallback row.
// It isolates a fresh, empty state directory so the disk is not contaminated by
// another test's persisted connections.
func TestConnectionListEmptyPlaceholder(t *testing.T) {
	t.Setenv("TABLEPRO_STATE_DIR", t.TempDir())
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	c.picker.list = container.NewVBox()
	c.refreshConnectionList()
	if got := len(c.picker.list.Objects); got != 1 {
		t.Fatalf("empty list rows = %d, want 1 (placeholder)", got)
	}
}

// TestConnectionListRowShowsName pins that each row carries the connection name,
// so the list reads as a list and not anonymous badges.
func TestConnectionListRowShowsName(t *testing.T) {
	t.Setenv("TABLEPRO_STATE_DIR", t.TempDir())
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	c.store.AddConnection(db.ConnConfig{ID: "c1", Name: "alpha", Type: db.DbTypeMySQL})
	c.picker.list = container.NewVBox()
	c.refreshConnectionList()

	if !findLabel(c.picker.list.Objects[0], "alpha") {
		t.Fatal("connection name 'alpha' not found in the list row")
	}
}

// TestViewIconDiffersFromTable pins the tree's view/table distinction: a view
// node must not use the same glyph as a base table, which is what lets the two
// be told apart at a glance.
func TestViewIconDiffersFromTable(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)

	tableNode := &nodeData{kind: "table", typ: "table", uid: "t1", label: "t"}
	viewNode := &nodeData{kind: "table", typ: "view", uid: "v1", label: "v"}

	tableIcon := findButtonIcon(c.schemaRowWidget(tableNode, 0))
	viewIcon := findButtonIcon(c.schemaRowWidget(viewNode, 0))
	if tableIcon == "" || viewIcon == "" {
		t.Fatalf("could not read icons: table=%q view=%q", tableIcon, viewIcon)
	}
	if tableIcon == viewIcon {
		t.Errorf("view and table share icon %q; they must differ", tableIcon)
	}
}

// TestDataBarBuilt pins that the TablePlus-style strip over the grid exists with
// its four actions and the name label after the query view is built.
func TestDataBarBuilt(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	c.buildQueryView()
	if c.dbar.insertRow == nil || c.dbar.copyRow == nil ||
		c.dbar.deleteRow == nil || c.dbar.reload == nil {
		t.Fatal("data bar row actions not all built")
	}
	if c.dataName == nil {
		t.Fatal("data bar name label not built")
	}
}

// findLabel walks the object tree for a Label whose text matches.
func findLabel(obj fyne.CanvasObject, want string) bool {
	switch n := obj.(type) {
	case *widget.Label:
		if n.Text == want {
			return true
		}
	case *fyne.Container:
		for _, o := range n.Objects {
			if findLabel(o, want) {
				return true
			}
		}
	case fyne.Widget:
		r := test.WidgetRenderer(n)
		if r == nil {
			return false
		}
		for _, o := range r.Objects() {
			if findLabel(o, want) {
				return true
			}
		}
	}
	return false
}

// findButtonIcon walks the object tree for the first button and returns its
// icon's resource name.
func findButtonIcon(obj fyne.CanvasObject) string {
	switch n := obj.(type) {
	case *widget.Button:
		if n.Icon != nil {
			return n.Icon.Name()
		}
		return ""
	case *fyne.Container:
		for _, o := range n.Objects {
			if s := findButtonIcon(o); s != "" {
				return s
			}
		}
	case fyne.Widget:
		r := test.WidgetRenderer(n)
		if r == nil {
			return ""
		}
		for _, o := range r.Objects() {
			if s := findButtonIcon(o); s != "" {
				return s
			}
		}
	}
	return ""
}
