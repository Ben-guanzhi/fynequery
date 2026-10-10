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
	c := &Client{store: db.NewStore(), tree: treeState{nodes: make(map[string]*nodeData)}}
	c.buildSchemaView()

	c.refreshSchema() // nothing connected
	if got := len(c.tree.rows.Objects); got != 1 {
		t.Fatalf("disconnected rows = %d, want 1 (root only)", got)
	}
	if btn := schemaRowButton(t, c.tree.rows.Objects[0]); btn.Text != "未连接" {
		t.Fatalf("root label = %q, want 未连接", btn.Text)
	}

	// Simulate a loaded tree: two dbs under the root; the root row itself is
	// hidden once there is content, matching the reference navigator.
	root := c.tree.nodes[treeRootUID]
	root.label = "数据库 (2)"
	root.lower = "数据库 (2)"
	root.children = []string{"db:a", "db:b"}
	c.tree.nodes["db:a"] = &nodeData{kind: "db", label: "a", lower: "a", db: "a", uid: "db:a"}
	c.tree.nodes["db:b"] = &nodeData{kind: "db", label: "b", lower: "b", db: "b", uid: "db:b"}
	c.rebuildSchemaRows()
	if got := len(c.tree.rows.Objects); got != 2 {
		t.Fatalf("rows with 2 dbs = %d, want 2 (root hidden)", got)
	}

	c.tree.nodes["db:a"].loaded = true
	c.tree.nodes["db:a"].children = []string{"db:a|t:ta"}
	c.tree.nodes["db:a|t:ta"] = &nodeData{kind: "table", label: "ta", lower: "ta", name: "ta", db: "a", uid: "db:a|t:ta"}
	c.tree.expanded["db:a"] = true
	c.rebuildSchemaRows()
	if got := len(c.tree.rows.Objects); got != 3 {
		t.Fatalf("rows with expanded db = %d, want 3", got)
	}
	// Rows: db:a, its expanded table, then db:b.
	if btn := schemaRowButton(t, c.tree.rows.Objects[1]); btn.Text != "ta" {
		t.Fatalf("table row = %q, want ta", btn.Text)
	}

	// The filter narrows the loaded objects, keeping the matching subtree.
	c.setTreeFilter("ta")
	if got := len(c.tree.rows.Objects); got != 2 { // db:a (ancestor context) + ta
		t.Fatalf("filtered rows = %d, want 2", got)
	}
	if btn := schemaRowButton(t, c.tree.rows.Objects[1]); btn.Text != "ta" {
		t.Fatalf("filtered table row = %q, want ta", btn.Text)
	}
	c.setTreeFilter("")
	if got := len(c.tree.rows.Objects); got != 3 {
		t.Fatalf("rows after clearing filter = %d, want 3", got)
	}

	// Tapping the table row fills the editor (offline: plain identifier).
	c.tabs.entry = newSuggestEntry()
	c.onSchemaTableTapped("db:a|t:ta")
	if got := c.tabs.entry.Text; got != "SELECT * FROM ta" {
		t.Fatalf("editor text = %q, want SELECT * FROM ta", got)
	}
	if c.tree.selected != "db:a|t:ta" {
		t.Fatalf("selectedNode = %q, want db:a|t:ta", c.tree.selected)
	}
}

// TestBranchRowsCarryMenu pins the affordance the navigator gained with
// 「新建表」: a database or schema row is wrapped for right-click, a plain
// status row is not.
func TestBranchRowsCarryMenu(t *testing.T) {
	test.NewApp()
	c := &Client{store: db.NewStore(), tree: treeState{nodes: make(map[string]*nodeData), expanded: make(map[string]bool)}}
	c.buildSchemaView()
	c.tree.nodes[treeRootUID] = &nodeData{kind: "root", label: "数据库", lower: "数据库", uid: treeRootUID}
	c.tree.nodes["db:a"] = &nodeData{kind: "db", label: "a", lower: "a", db: "a", uid: "db:a"}
	c.tree.nodes["db:a|s:pub"] = &nodeData{kind: "schema", label: "pub", lower: "pub", db: "a", schema: "pub", uid: "db:a|s:pub"}
	c.tree.nodes["root|error"] = &nodeData{kind: "error", label: "加载失败", lower: "加载失败", uid: "root|error"}

	for _, uid := range []string{"db:a", "db:a|s:pub"} {
		if _, ok := c.schemaRowWidget(c.tree.nodes[uid], 0).(*tapMenuRow); !ok {
			t.Errorf("%s row must carry the right-click menu", uid)
		}
	}
	if _, ok := c.schemaRowWidget(c.tree.nodes["root|error"], 0).(*tapMenuRow); ok {
		t.Error("an error row has no node actions")
	}
}

// TestSchemaRowCacheReuse pins the navigator memoization: a rebuild whose
// rows are unchanged reuses the rendered widgets and the identical list
// skips the re-layout, while any visual input change produces a new widget.
func TestSchemaRowCacheReuse(t *testing.T) {
	test.NewApp()
	c := &Client{store: db.NewStore(), tree: treeState{nodes: make(map[string]*nodeData), expanded: make(map[string]bool)}}
	c.buildSchemaView()
	c.tree.nodes[treeRootUID] = &nodeData{kind: "root", label: "数据库", lower: "数据库", uid: treeRootUID}
	root := c.tree.nodes[treeRootUID]
	root.label = "数据库 (1)"
	root.lower = "数据库 (1)"
	root.children = []string{"db:a"}
	c.tree.nodes["db:a"] = &nodeData{kind: "db", label: "a", lower: "a", db: "a", uid: "db:a"}
	c.tree.nodes["db:a"].loaded = true
	c.tree.nodes["db:a"].children = []string{"db:a|t:ta"}
	c.tree.nodes["db:a|t:ta"] = &nodeData{kind: "table", label: "ta", lower: "ta", name: "ta", db: "a", uid: "db:a|t:ta"}
	c.tree.expanded["db:a"] = true
	c.rebuildSchemaRows()

	first := append([]fyne.CanvasObject(nil), c.tree.rows.Objects...)
	c.rebuildSchemaRows()
	if !sameRowList(first, c.tree.rows.Objects) {
		t.Fatal("unchanged rebuild must reuse the exact row widgets")
	}

	c.tree.selected = "db:a|t:ta"
	c.rebuildSchemaRows()
	if len(c.tree.rows.Objects) != 2 || c.tree.rows.Objects[1] == first[1] {
		t.Fatal("selecting a row must render a new widget for it")
	}

	c.tree.selected = ""
	c.tree.nodes["db:a|t:ta"].label = "ta [table]"
	c.tree.nodes["db:a|t:ta"].lower = "ta [table]"
	c.rebuildSchemaRows()
	if len(c.tree.rows.Objects) != 2 || c.tree.rows.Objects[1] == first[1] {
		t.Fatal("relabeled node must render a new widget")
	}
}

// schemaRowButtonOf returns the row's button without test failure semantics.
func schemaRowButtonOf(row fyne.CanvasObject) *widget.Button {
	var box *fyne.Container
	switch r := row.(type) {
	case *tapMenuRow: // rows with a right-click menu carry this wrapper
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
	case *tapMenuRow: // rows with a right-click menu carry this wrapper
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
