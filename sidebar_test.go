package main

import (
	"reflect"
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

// TestSidebarKVMode checks the key/value flavour of the tree: only the 键 group,
// a "* ? [" capable search forwarded to the session, and no local re-filtering
// (the server already applied the MATCH pattern).
func TestSidebarKVMode(t *testing.T) {
	test.NewApp()
	var searched []string
	s := newSidebar(nil, nil)
	s.kv = true
	s.onSearch = func(text string) { searched = append(searched, text) }
	s.setInventory([]TableInfo{
		{Name: "user:1", Kind: "table"},
		{Name: "user:2", Kind: "table"},
	}, "0")
	test.WidgetRenderer(s)

	if got := s.rootGroups(); len(got) != 1 || got[0] != nodeTables {
		t.Fatalf("rootGroups() = %v, want [%s]", got, nodeTables)
	}
	if got := s.branchLabel(nodeTables); got != "键" {
		t.Fatalf("branchLabel(nodeTables) = %q, want 键", got)
	}

	s.search.SetText("user:*")
	if len(searched) != 1 || searched[0] != "user:*" {
		t.Fatalf("onSearch = %v, want [user:*]", searched)
	}
	// The glob is not a substring of the returned keys, so a local filter
	// would blank the tree; the server-side result must survive verbatim.
	s.applyFilter()
	if len(s.filtered) != 2 {
		t.Fatalf("kv applyFilter dropped server hits: %v", s.filtered)
	}

	s.search.SetText("")
	if len(searched) != 2 || searched[1] != "" {
		t.Fatalf("clearing search: onSearch = %v, want trailing empty", searched)
	}
}

// TestSidebarKVLoadMore checks the paged KV inventory: the "load more" row
// appears only while the session holds another page, selecting it loads instead
// of opening an object, and group/pseudo rows never open the item menu.
func TestSidebarKVLoadMore(t *testing.T) {
	test.NewApp()
	var opened []string
	s := newSidebar(func(name string) { opened = append(opened, name) }, nil)
	s.kv = true
	more := 0
	s.onMoreKeys = func() { more++ }
	test.WidgetRenderer(s) // builds the tree and the search box
	s.setInventory([]TableInfo{{Name: "user:1", Kind: "table"}}, "0")

	if got := s.childIDs(nodeTables); !reflect.DeepEqual(got, []string{"user:1"}) {
		t.Fatalf("childIDs without more = %v", got)
	}

	s.setMoreKeys(true)
	if got := s.childIDs(nodeTables); !reflect.DeepEqual(got, []string{"user:1", nodeMore}) {
		t.Fatalf("childIDs with more = %v", got)
	}
	if got := s.branchLabel(nodeMore); got != "加载更多键…" {
		t.Fatalf("branchLabel(nodeMore) = %q", got)
	}

	s.tree.Select(nodeMore)
	if more != 1 {
		t.Fatalf("selecting the more row fired onMoreKeys %d times, want 1", more)
	}
	if len(opened) != 0 {
		t.Fatalf("selecting the more row opened %v", opened)
	}
	if s.selID != "" {
		t.Fatalf("more row stayed selected (%q)", s.selID)
	}

	s.setMoreKeys(false)
	if got := s.childIDs(nodeTables); !reflect.DeepEqual(got, []string{"user:1"}) {
		t.Fatalf("childIDs after exhausting the keyspace = %v", got)
	}

	var menuIDs []string
	s.onItemMenu = func(name string, _ fyne.Position) { menuIDs = append(menuIDs, name) }
	pos := fyne.NewPos(0, 0)
	s.itemSecondary("user:1", pos)
	s.itemSecondary(nodeTables, pos)
	s.itemSecondary(nodeMore, pos)
	s.itemSecondary("", pos)
	if !reflect.DeepEqual(menuIDs, []string{"user:1"}) {
		t.Fatalf("item menu fired for %v, want only user:1", menuIDs)
	}
}

// TestSidebarSQLStillFiltersLocally guards the non-KV path: searching narrows
// the already-loaded table list without involving the session.
func TestSidebarSQLStillFiltersLocally(t *testing.T) {
	test.NewApp()
	s := newSidebar(nil, nil)
	s.setInventory([]TableInfo{
		{Name: "orders", Kind: "table"},
		{Name: "users", Kind: "table"},
	}, "shop")
	test.WidgetRenderer(s)

	s.search.SetText("use")
	s.applyFilter()
	if len(s.filtered) != 1 || s.filtered[0].Name != "users" {
		t.Fatalf("filtered = %v, want [users]", s.filtered)
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
