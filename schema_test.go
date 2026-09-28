package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// TestSchemaRowsModel drives the flat-row navigator: the root row is shown
// even when disconnected, open branches render their children, and tapping a
// table row fills the SQL editor.
func TestSchemaRowsModel(t *testing.T) {
	test.NewApp()
	c := &Client{store: db.NewStore(), treeNodes: make(map[string]*nodeData)}
	c.buildSchemaView()

	c.refreshSchema() // nothing connected
	if got := len(c.schemaRows.Objects); got != 1 {
		t.Fatalf("disconnected rows = %d, want 1 (root only)", got)
	}
	if btn := schemaRowButton(t, c.schemaRows.Objects[0]); btn.Text != "未连接" {
		t.Fatalf("root label = %q, want 未连接", btn.Text)
	}

	// Simulate a loaded tree: two dbs under the root; the root row itself is
	// hidden once there is content, matching the reference navigator.
	root := c.treeNodes[treeRootUID]
	root.label = "数据库 (2)"
	root.children = []string{"db:a", "db:b"}
	c.treeNodes["db:a"] = &nodeData{kind: "db", label: "a", db: "a", uid: "db:a"}
	c.treeNodes["db:b"] = &nodeData{kind: "db", label: "b", db: "b", uid: "db:b"}
	c.rebuildSchemaRows()
	if got := len(c.schemaRows.Objects); got != 2 {
		t.Fatalf("rows with 2 dbs = %d, want 2 (root hidden)", got)
	}

	c.treeNodes["db:a"].loaded = true
	c.treeNodes["db:a"].children = []string{"db:a|t:ta"}
	c.treeNodes["db:a|t:ta"] = &nodeData{kind: "table", label: "ta", name: "ta", db: "a", uid: "db:a|t:ta"}
	c.expanded["db:a"] = true
	c.rebuildSchemaRows()
	if got := len(c.schemaRows.Objects); got != 3 {
		t.Fatalf("rows with expanded db = %d, want 3", got)
	}
	// Rows: db:a, its expanded table, then db:b.
	if btn := schemaRowButton(t, c.schemaRows.Objects[1]); btn.Text != "ta" {
		t.Fatalf("table row = %q, want ta", btn.Text)
	}

	// The filter narrows the loaded objects, keeping the matching subtree.
	c.setTreeFilter("ta")
	if got := len(c.schemaRows.Objects); got != 2 { // db:a (ancestor context) + ta
		t.Fatalf("filtered rows = %d, want 2", got)
	}
	if btn := schemaRowButton(t, c.schemaRows.Objects[1]); btn.Text != "ta" {
		t.Fatalf("filtered table row = %q, want ta", btn.Text)
	}
	c.setTreeFilter("")
	if got := len(c.schemaRows.Objects); got != 3 {
		t.Fatalf("rows after clearing filter = %d, want 3", got)
	}

	// Tapping the table row fills the editor (offline: plain identifier).
	c.queryEntry = newSuggestEntry()
	c.onSchemaTableTapped("db:a|t:ta")
	if got := c.queryEntry.Text; got != "SELECT * FROM ta" {
		t.Fatalf("editor text = %q, want SELECT * FROM ta", got)
	}
	if c.selectedNode != "db:a|t:ta" {
		t.Fatalf("selectedNode = %q, want db:a|t:ta", c.selectedNode)
	}
}

// TestSchemaRowCacheReuse pins the navigator memoization: a rebuild whose
// rows are unchanged reuses the rendered widgets and the identical list
// skips the re-layout, while any visual input change produces a new widget.
func TestSchemaRowCacheReuse(t *testing.T) {
	test.NewApp()
	c := &Client{store: db.NewStore(), treeNodes: make(map[string]*nodeData), expanded: make(map[string]bool)}
	c.buildSchemaView()
	c.treeNodes[treeRootUID] = &nodeData{kind: "root", label: "数据库", uid: treeRootUID}
	root := c.treeNodes[treeRootUID]
	root.label = "数据库 (1)"
	root.children = []string{"db:a"}
	c.treeNodes["db:a"] = &nodeData{kind: "db", label: "a", db: "a", uid: "db:a"}
	c.treeNodes["db:a"].loaded = true
	c.treeNodes["db:a"].children = []string{"db:a|t:ta"}
	c.treeNodes["db:a|t:ta"] = &nodeData{kind: "table", label: "ta", name: "ta", db: "a", uid: "db:a|t:ta"}
	c.expanded["db:a"] = true
	c.rebuildSchemaRows()

	first := append([]fyne.CanvasObject(nil), c.schemaRows.Objects...)
	c.rebuildSchemaRows()
	if !sameRowList(first, c.schemaRows.Objects) {
		t.Fatal("unchanged rebuild must reuse the exact row widgets")
	}

	c.selectedNode = "db:a|t:ta"
	c.rebuildSchemaRows()
	if len(c.schemaRows.Objects) != 2 || c.schemaRows.Objects[1] == first[1] {
		t.Fatal("selecting a row must render a new widget for it")
	}

	c.selectedNode = ""
	c.treeNodes["db:a|t:ta"].label = "ta [table]"
	c.rebuildSchemaRows()
	if len(c.schemaRows.Objects) != 2 || c.schemaRows.Objects[1] == first[1] {
		t.Fatal("relabeled node must render a new widget")
	}
}

// schemaRowButtonOf returns the row's button without test failure semantics.
func schemaRowButtonOf(row fyne.CanvasObject) *widget.Button {
	var box *fyne.Container
	switch r := row.(type) {
	case *tapMenuRow: // table rows carry the right-click menu wrapper
		box = r.row
	case *fyne.Container:
		box = r
	}
	if box == nil {
		return nil
	}
	for _, obj := range box.Objects { // Border stores the center object first
		if btn, ok := obj.(*widget.Button); ok {
			return btn
		}
	}
	return nil
}

func schemaRowButton(t *testing.T, row fyne.CanvasObject) *widget.Button {
	t.Helper()
	var box *fyne.Container
	switch r := row.(type) {
	case *tapMenuRow: // table rows carry the right-click menu wrapper
		box = r.row
	case *fyne.Container:
		box = r
	default:
		t.Fatal("row is not a container")
	}
	for _, obj := range box.Objects { // Border stores the center object first
		if btn, ok := obj.(*widget.Button); ok {
			return btn
		}
	}
	t.Fatal("row has no button")
	return nil
}
