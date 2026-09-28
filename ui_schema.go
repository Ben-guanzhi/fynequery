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
	c.schemaRows = container.NewVBox()
	c.treeFilter = ""

	newBtn := widget.NewButtonWithIcon("", theme.ContentAddIcon(), func() {
		c.showConnDialog(nil)
	})
	newBtn.Importance = widget.LowImportance
	header := container.NewBorder(nil, nil, nil, newBtn,
		widget.NewLabelWithStyle("导航", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))

	c.filterEntry = widget.NewEntry()
	c.filterEntry.PlaceHolder = "过滤已加载对象"
	c.filterEntry.OnChanged = func(string) { c.setTreeFilter(c.filterEntry.Text) }
	refreshBtn := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), func() {
		c.refreshSchema()
	})
	refreshBtn.Importance = widget.LowImportance
	searchBtn := widget.NewButtonWithIcon("", theme.SearchIcon(), func() {
		c.showSearchDialog()
	})
	searchBtn.Importance = widget.LowImportance
	filterRow := container.NewBorder(nil, nil, nil,
		container.NewHBox(searchBtn, refreshBtn), c.filterEntry)

	tree := container.NewBorder(filterRow, nil, nil, nil,
		container.NewScroll(c.schemaRows))

	// A VBox would hand the tree only its minimum height and leave dead space
	// below (fyne boxes do not stretch children); Border makes the tree absorb
	// all the remaining sidebar height.
	return container.NewBorder(header, nil, nil, nil, tree)
}

// expandDatabaseInTree loads and opens the database chosen in the dropdown,
// so the navigator always mirrors "当前库" after a switch.
func (c *Client) expandDatabaseInTree(db string) {
	if db == "" || c.schemaRows == nil {
		return
	}
	uid := "db:" + db
	nd := c.treeNodes[uid]
	if nd == nil {
		return // not connected, or the db is not visible to this login
	}
	nd.loaded = false
	nd.children = nil
	c.expanded[uid] = true
	c.loadChildren(nd)
	c.rebuildSchemaRows()
}

// setTreeFilter narrows the navigator to the loaded objects whose label
// contains the filter, keeping the hierarchy of matching subtrees.
func (c *Client) setTreeFilter(filter string) {
	c.treeFilter = strings.ToLower(strings.TrimSpace(filter))
	c.rebuildSchemaRows()
}

// rebuildSchemaRows re-renders the navigator. Unfiltered, it walks the open
// branches from the root - whose own row only appears while there is nothing
// to list (未连接 / 加载失败). With a filter, matching subtrees keep their
// hierarchy and everything else is pruned.
func (c *Client) rebuildSchemaRows() {
	if c.schemaRows == nil {
		return
	}
	c.schemaRows.Objects = nil
	switch {
	case c.treeFilter != "":
		for _, uid := range c.treeNodes[treeRootUID].children {
			if c.filterHasMatch(uid) {
				c.appendFilteredRow(uid, 0)
			}
		}
	case len(c.treeNodes[treeRootUID].children) == 0:
		c.appendSchemaRow(treeRootUID, 0) // 未连接 / 加载失败 / 空目录
	default:
		for _, uid := range c.treeNodes[treeRootUID].children {
			c.appendSchemaRow(uid, 0)
		}
	}
	c.schemaRows.Refresh()
}

// filterHasMatch reports whether the subtree rooted at uid contains a label
// matching the current filter.
func (c *Client) filterHasMatch(uid string) bool {
	nd, ok := c.treeNodes[uid]
	if !ok {
		return false
	}
	if strings.Contains(strings.ToLower(nd.label), c.treeFilter) {
		return true
	}
	for _, child := range nd.children {
		if c.filterHasMatch(child) {
			return true
		}
	}
	return false
}

// appendFilteredRow appends a matching subtree; ancestors of a match stay
// visible for context, their unmatched siblings are pruned. Callers guard
// with filterHasMatch, so the subtree is known to contain a match.
func (c *Client) appendFilteredRow(uid string, depth int) {
	nd, ok := c.treeNodes[uid]
	if !ok {
		return
	}
	c.schemaRows.Objects = append(c.schemaRows.Objects, c.schemaRowWidget(nd, depth))
	for _, child := range nd.children {
		if c.filterHasMatch(child) {
			c.appendFilteredRow(child, depth+1)
		}
	}
}

// appendSchemaRow appends one row and recurses into open branches. The root
// is always expanded.
func (c *Client) appendSchemaRow(uid string, depth int) {
	nd, ok := c.treeNodes[uid]
	if !ok {
		return
	}
	c.schemaRows.Objects = append(c.schemaRows.Objects, c.schemaRowWidget(nd, depth))
	if nd.kind != "root" && !c.expanded[uid] {
		return
	}
	for _, child := range nd.children {
		c.appendSchemaRow(child, depth+1)
	}
}

// schemaRowWidget builds one navigator row: a fixed indent, then a
// full-width leading-aligned button so the whole row is clickable and its
// label reports the true min width (which drives the horizontal scrollbar).
func (c *Client) schemaRowWidget(nd *nodeData, depth int) fyne.CanvasObject {
	var icon fyne.Resource
	switch nd.kind {
	case "empty":
		icon = theme.InfoIcon()
	case "error":
		icon = theme.WarningIcon()
	case "db":
		icon = theme.StorageIcon()
	case "schema":
		if c.expanded[nd.uid] {
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
	if nd.uid == c.selectedNode {
		btn.Importance = widget.HighImportance
	}

	row := container.NewBorder(nil, nil, schemaIndentWidget(depth), nil, btn)
	if nd.kind != "table" {
		return row
	}
	// Right-click a table/view row for its catalog actions.
	uid := nd.uid
	return newTapMenuRow(row, func(pos fyne.Position) {
		c.showTableNodeMenu(uid, pos)
	})
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
	nd, ok := c.treeNodes[uid]
	if !ok {
		return
	}
	if c.expanded[uid] {
		delete(c.expanded, uid)
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
		c.expanded[uid] = true
		c.clearTreeFilter()
		c.rebuildSchemaRows()
		return
	}
	errorChild := false
	for _, ch := range nd.children {
		if n := c.treeNodes[ch]; n != nil && n.kind == "error" {
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
	c.expanded[uid] = true
	c.clearTreeFilter()
	c.rebuildSchemaRows()
}

// clearTreeFilter resets the navigator filter, syncing the entry box.
func (c *Client) clearTreeFilter() {
	c.treeFilter = ""
	if c.filterEntry != nil && c.filterEntry.Text != "" {
		c.filterEntry.SetText("") // fires OnChanged, which rebuilds the rows
		return
	}
	c.rebuildSchemaRows()
}
