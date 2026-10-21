package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"fynequery/proto"
)

// TestTreeItemSecondary exercises the right-click path that opens the sidebar
// item context menu.
func TestTreeItemSecondary(t *testing.T) {
	test.NewApp()
	it := newTreeItem()
	it.id = "orders"

	var gotID string
	var gotPos fyne.Position
	it.onSecondary = func(id string, pos fyne.Position) {
		gotID = id
		gotPos = pos
	}
	test.TapSecondary(it)
	if gotID != "orders" {
		t.Fatalf("secondary tap id = %q, want orders", gotID)
	}
	_ = gotPos

	// Branch/empty ids must not open a menu.
	it.id = ""
	gotID = ""
	test.TapSecondary(it)
	if gotID != "" {
		t.Fatalf("empty id should not fire the menu, got %q", gotID)
	}
}

// TestTreeItemPrimary guards the left-click path. treeItem implements
// SecondaryTappable, so it is the deepest object Fyne's hit test matches for a
// row; if it were not also Tappable the click would be swallowed and never
// reach the tree node's Select/OnSelected (so tables would not open).
func TestTreeItemPrimary(t *testing.T) {
	test.NewApp()
	it := newTreeItem()
	it.id = "orders"

	var gotID string
	it.onTap = func(id string) { gotID = id }
	test.Tap(it)
	if gotID != "orders" {
		t.Fatalf("primary tap id = %q, want orders", gotID)
	}

	// Branch/empty ids must not be forwarded.
	it.id = ""
	gotID = ""
	test.Tap(it)
	if gotID != "" {
		t.Fatalf("empty id should not fire onTap, got %q", gotID)
	}
}

// TestSidebarSelectionOpensTable checks the tree's selection wiring: selecting
// a table fires onSelect (which opens it), while selecting a branch does not.
func TestSidebarSelectionOpensTable(t *testing.T) {
	test.NewApp()
	var got []string
	s := newSidebar(func(name string) { got = append(got, name) }, nil)
	s.setInventory([]TableInfo{{Name: "users", Kind: "table"}}, "shop")

	test.WidgetRenderer(s) // builds the tree
	if s.tree == nil {
		t.Fatal("tree not constructed")
	}

	s.tree.Select("users")
	if len(got) != 1 || got[0] != "users" {
		t.Fatalf("select table: onSelect = %v, want [users]", got)
	}

	s.tree.Select(nodeTables) // a branch: must not open anything
	if len(got) != 1 {
		t.Fatalf("select branch fired onSelect: %v", got)
	}
}

// TestBuildCreateScript checks the generated CREATE TABLE for MySQL quoting and
// the primary-key constraint.
func TestBuildCreateScript(t *testing.T) {
	cols := []proto.ColumnInfo{
		{Name: "id", Type: "int", Key: "PK"},
		{Name: "name", Type: "varchar(255)", Nullable: true},
	}
	got := buildCreateScript("orders", "`orders`", cols, "mysql")
	for _, want := range []string{"CREATE TABLE `orders`", "`id` int NOT NULL", "`name` varchar(255)", "PRIMARY KEY (`id`)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CREATE script missing %q:\n%s", want, got)
		}
	}
}

// TestCloneAndTruncateSQL checks the dialect-specific clone/truncate scripts.
func TestCloneAndTruncateSQL(t *testing.T) {
	ws := &workspace{conn: Connection{Driver: "mysql"}}
	if got := ws.truncateSQL("t", "`t`"); got != "TRUNCATE TABLE `t`;" {
		t.Fatalf("mysql truncate = %q", got)
	}
	if got := ws.cloneSQL("t", "`t`", "t2", "`t2`"); !strings.Contains(got, "CREATE TABLE `t2` LIKE `t`") {
		t.Fatalf("mysql clone = %q", got)
	}

	ws = &workspace{conn: Connection{Driver: "sqlite"}}
	if got := ws.truncateSQL("t", `"t"`); got != `DELETE FROM "t";` {
		t.Fatalf("sqlite truncate = %q", got)
	}
	if got := ws.cloneSQL("t", `"t"`, "t2", `"t2"`); !strings.Contains(got, "CREATE TABLE \"t2\" AS SELECT * FROM \"t\"") {
		t.Fatalf("sqlite clone = %q", got)
	}
}
