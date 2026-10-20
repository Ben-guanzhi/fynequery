package main

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// schemaIndent is the per-depth padding of navigator rows.
const schemaIndent = float32(16.0)

// buildSchemaView assembles the navigator, in the shape of TablePro Go's
// sidebar: a "导航" header with the add-connection button, the connection
// picker above it lives in the connections panel, a filter box that narrows
// the loaded objects, and the row list itself. fyne's Tree sizes its
// scrollable area from prototype rows and never from real node labels, so
// long database/table names could not be scrolled into view; a flat row list
// inside a two-way Scroll derives its width from actual content.
func (c *Client) buildSchemaView() fyne.CanvasObject {
	c.tree.rows = container.NewVBox()
	c.tree.term = ""

	newInner := widget.NewButtonWithIcon("", theme.ContentAddIcon(), func() {
		c.showConnDialog(nil)
	})
	newInner.Importance = widget.LowImportance
	newBtn := attachTooltip(newInner, "新建连接")
	header := container.NewBorder(nil, nil, nil, newBtn,
		widget.NewLabelWithStyle("导航", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))

	c.tree.filterEntry = newKeyedEntry()
	c.tree.filterEntry.PlaceHolder = "过滤已加载对象"
	c.tree.filterEntry.onShortcut = c.entryShortcut
	c.tree.filterEntry.OnChanged = func(string) { c.setTreeFilter(c.tree.filterEntry.Text) }
	refreshInner := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), func() {
		c.refreshSchema()
	})
	refreshInner.Importance = widget.LowImportance
	refreshBtn := attachTooltip(refreshInner, "重新加载结构树")
	searchInner := widget.NewButtonWithIcon("", theme.SearchIcon(), func() {
		c.showSearchDialog()
	})
	searchInner.Importance = widget.LowImportance
	searchBtn := attachTooltip(searchInner, "按名称跨库快速找表（Ctrl+P）")
	filterRow := container.NewBorder(nil, nil, nil,
		container.NewHBox(searchBtn, refreshBtn), c.tree.filterEntry)

	tree := container.NewBorder(filterRow, nil, nil, nil,
		container.NewScroll(c.tree.rows))

	// A VBox would hand the tree only its minimum height and leave dead space
	// below (fyne boxes do not stretch children); Border makes the tree absorb
	// all the remaining sidebar height.
	return container.NewBorder(header, nil, nil, nil, tree)
}

// expandDatabaseInTree loads and opens the database chosen in the dropdown,
// so the navigator always mirrors "当前库" after a switch.
func (c *Client) expandDatabaseInTree(db string) {
	if db == "" || c.tree.rows == nil {
		return
	}
	uid := "db:" + db
	nd := c.tree.nodes[uid]
	if nd == nil {
		return // not connected, or the db is not visible to this login
	}
	nd.loaded = false
	nd.children = nil
	c.tree.expanded[uid] = true
	c.loadChildren(nd)
	c.rebuildSchemaRows()
}

// setTreeFilter narrows the navigator to the loaded objects whose label
// contains the filter, keeping the hierarchy of matching subtrees.
func (c *Client) setTreeFilter(filter string) {
	c.tree.term = strings.ToLower(strings.TrimSpace(filter))
	c.rebuildSchemaRows()
}

// rowCacheKey identifies a rendered navigator row by everything its widget
// reads; two rebuilds that hit the same key share one widget instance.
type rowCacheKey struct {
	uid      string
	depth    int
	label    string
	expanded bool
	selected bool
}

// rebuildSchemaRows re-renders the navigator. Unfiltered, it walks the open
// branches from the root - whose own row only appears while there is nothing
// to list (未连接 / 加载失败). With a filter, matching subtrees keep their
// hierarchy and everything else is pruned. The filtered pass marks every
// node in ONE subtree walk (each label is lowered exactly once), and the
// finished row list is compared against what is on screen: an identical
// list skips the re-layout, which dominates the cost of a keystroke.
func (c *Client) rebuildSchemaRows() {
	if c.tree.rows == nil {
		return
	}
	var rows []fyne.CanvasObject
	switch {
	case c.tree.term != "":
		marks := make(map[string]bool, len(c.tree.nodes))
		for _, uid := range c.tree.nodes[treeRootUID].children {
			c.markFilteredSubtree(uid, marks)
		}
		for _, uid := range c.tree.nodes[treeRootUID].children {
			if marks[uid] {
				rows = c.appendFilteredRow(rows, uid, 0, marks)
			}
		}
	case len(c.tree.nodes[treeRootUID].children) == 0:
		rows = c.appendSchemaRow(rows, treeRootUID, 0) // 未连接 / 加载失败 / 空目录
	default:
		for _, uid := range c.tree.nodes[treeRootUID].children {
			rows = c.appendSchemaRow(rows, uid, 0)
		}
	}
	if sameRowList(c.tree.rows.Objects, rows) {
		return
	}
	c.tree.rows.Objects = rows
	c.tree.rows.Refresh()
}

// markFilteredSubtree records every node of the subtree that must stay
// visible under the current filter (its own label matches, or a descendant
// makes its subtree relevant) and reports the subtree's relevance.
func (c *Client) markFilteredSubtree(uid string, marks map[string]bool) bool {
	nd, ok := c.tree.nodes[uid]
	if !ok {
		return false
	}
	lower := nd.lower
	if lower == "" {
		lower = strings.ToLower(nd.label) // 防御：漏填 lower 时退化为旧行为
	}
	match := strings.Contains(lower, c.tree.term)
	for _, child := range nd.children {
		if c.markFilteredSubtree(child, marks) {
			match = true
		}
	}
	if match {
		marks[uid] = true
	}
	return match
}

// appendFilteredRow appends a matching subtree; ancestors of a match stay
// visible for context, their unmatched siblings are pruned. Callers consult
// marks, so the subtree is known to contain a match.
func (c *Client) appendFilteredRow(rows []fyne.CanvasObject, uid string, depth int, marks map[string]bool) []fyne.CanvasObject {
	nd, ok := c.tree.nodes[uid]
	if !ok {
		return rows
	}
	rows = append(rows, c.schemaRowWidget(nd, depth))
	for _, child := range nd.children {
		if marks[child] {
			rows = c.appendFilteredRow(rows, child, depth+1, marks)
		}
	}
	return rows
}

// appendSchemaRow appends one row and recurses into open branches. The root
// is always expanded.
func (c *Client) appendSchemaRow(rows []fyne.CanvasObject, uid string, depth int) []fyne.CanvasObject {
	nd, ok := c.tree.nodes[uid]
	if !ok {
		return rows
	}
	rows = append(rows, c.schemaRowWidget(nd, depth))
	if nd.kind != "root" && !c.tree.expanded[uid] {
		return rows
	}
	for _, child := range nd.children {
		rows = c.appendSchemaRow(rows, child, depth+1)
	}
	return rows
}

// sameRowList reports whether the two lists are pointer-identical: every row
// already on screen is exactly the row the rebuild produced, in order.
func sameRowList(a, b []fyne.CanvasObject) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// schemaRowWidget builds one navigator row: a fixed indent, then a
// full-width leading-aligned button so the whole row is clickable and its
// label reports the true min width (which drives the horizontal scrollbar).
// The result is memoized: building a row costs ~0.5µs and re-rendering it
// costs fyne ~100µs, so rebuilding the same visible tree (every keystroke in
// the filter box, every re-expansion) must not recreate widgets that have
// not changed. The key carries every input the row reads: identity, depth,
// label, whether its branch is open, and whether it is the highlighted node.
func (c *Client) schemaRowWidget(nd *nodeData, depth int) fyne.CanvasObject {
	expanded := c.tree.expanded[nd.uid]
	key := rowCacheKey{uid: nd.uid, depth: depth, label: nd.label, expanded: expanded, selected: nd.uid == c.tree.selected}
	if row, ok := c.tree.cache[key]; ok {
		return row
	}

	var icon fyne.Resource
	switch nd.kind {
	case "empty":
		icon = theme.InfoIcon()
	case "error":
		icon = theme.WarningIcon()
	case "db":
		icon = theme.StorageIcon()
	case "schema":
		if expanded {
			icon = theme.FolderOpenIcon()
		} else {
			icon = theme.FolderIcon()
		}
	case "table":
		icon = theme.DocumentIcon()
	}

	btn := widget.NewButtonWithIcon(nd.label, icon, nil)
	btn.Alignment = widget.ButtonAlignLeading
	btn.Importance = widget.LowImportance
	switch nd.kind {
	case "root":
		btn.Importance = widget.MediumImportance
	case "empty":
		btn.Importance = widget.LowImportance
		btn.Disable()
	case "error":
		parent := nd.parent
		btn.Importance = widget.DangerImportance
		btn.OnTapped = func() { c.retrySchemaNode(parent) }
	case "table":
		uid := nd.uid
		btn.OnTapped = func() { c.onSchemaTableTapped(uid) }
	default: // db / schema branch: tap toggles open/closed
		uid := nd.uid
		btn.OnTapped = func() { c.toggleSchemaNode(uid) }
	}
	if nd.uid == c.tree.selected {
		btn.Importance = widget.HighImportance
	}

	row := container.NewBorder(nil, nil, schemaIndentWidget(depth), nil, btn)
	var out fyne.CanvasObject = row
	switch nd.kind {
	case "table":
		// Right-click a table/view row for its catalog actions.
		uid := nd.uid
		out = newTapMenuRow(row, func(pos fyne.Position) {
			c.showTableNodeMenu(uid, pos)
		})
	case "db", "schema":
		// A branch row has one action of its own, aimed at the node it is on.
		uid := nd.uid
		out = newTapMenuRow(row, func(pos fyne.Position) {
			c.showBranchNodeMenu(uid, pos)
		})
	}
	if c.tree.cache == nil {
		c.tree.cache = make(map[rowCacheKey]fyne.CanvasObject)
	}
	c.tree.cache[key] = out
	return out
}

// tapMenuRow is a navigator row that opens the object menu on right-click.
// It must be a real fyne.Widget: the canvas walk only descends into
// *fyne.Container and fyne.Widget, and a bare struct wrapper would leave the
// row's button unrendered and unhittable.
type tapMenuRow struct {
	widget.BaseWidget
	row      *fyne.Container
	showMenu func(pos fyne.Position)
}

func newTapMenuRow(row *fyne.Container, showMenu func(fyne.Position)) *tapMenuRow {
	t := &tapMenuRow{row: row, showMenu: showMenu}
	t.ExtendBaseWidget(t)
	return t
}

// CreateRenderer implements fyne.Widget.
func (t *tapMenuRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(t.row)
}

// SecondaryTapped implements fyne.SecondaryTappable.
func (t *tapMenuRow) SecondaryTapped(pe *fyne.PointEvent) {
	t.showMenu(pe.AbsolutePosition)
}

// schemaIndentWidget returns a transparent fixed-width spacer for a depth
// level, or nil at the root.
func schemaIndentWidget(depth int) fyne.CanvasObject {
	if depth <= 0 {
		return nil
	}
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(schemaIndent*float32(depth), 0))
	return r
}

// toggleSchemaNode opens or closes a db/schema branch, lazily loading its
// children from the server on first open. Opening a branch clears the filter:
// the user has found what they were looking for, and a filter that prunes the
// just-loaded children made every expansion look empty.
func (c *Client) toggleSchemaNode(uid string) {
	nd, ok := c.tree.nodes[uid]
	if !ok {
		return
	}
	if c.tree.expanded[uid] {
		delete(c.tree.expanded, uid)
		c.rebuildSchemaRows()
		return
	}
	if !nd.loaded {
		c.setStatus("加载 " + nd.label + " ...")
		c.loadChildren(nd)
	}
	if nd.loading {
		// Async fetch in flight; mark the node open now and let
		// applyChildren re-render once its children arrive.
		c.tree.expanded[uid] = true
		c.clearTreeFilter()
		c.rebuildSchemaRows()
		return
	}
	errorChild := false
	for _, ch := range nd.children {
		if n := c.tree.nodes[ch]; n != nil && n.kind == "error" {
			errorChild = true
		}
	}
	switch {
	case len(nd.children) == 0:
		c.setStatus(nd.label + " 下没有可展开的内容")
	case errorChild:
		// keep the failure message set by addErrorNode; the red row retries
	default:
		c.setStatus("就绪")
	}
	c.tree.expanded[uid] = true
	c.clearTreeFilter()
	c.rebuildSchemaRows()
}

// clearTreeFilter resets the navigator filter, syncing the entry box.
func (c *Client) clearTreeFilter() {
	c.tree.term = ""
	if c.tree.filterEntry != nil && c.tree.filterEntry.Text != "" {
		c.tree.filterEntry.SetText("") // fires OnChanged, which rebuilds the rows
		return
	}
	c.rebuildSchemaRows()
}
