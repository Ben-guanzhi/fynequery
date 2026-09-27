package main

import (
	"fmt"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
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

// showTableNodeMenu pops the drop/truncate/rename menu for one table row.
func (c *Client) showTableNodeMenu(uid string, pos fyne.Position) {
	nd, ok := c.treeNodes[uid]
	if !ok {
		return
	}
	items := []*fyne.MenuItem{
		fyne.NewMenuItem("查看结构", func() { c.showStructureDialog(nd) }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("删除 (DROP)", func() { c.confirmNodeAction(nd, actionDrop, "") }),
	}
	if nd.typ != "view" {
		items = append(items, fyne.NewMenuItem("清空 (TRUNCATE)", func() {
			c.confirmNodeAction(nd, actionTruncate, "")
		}))
	}
	items = append(items, fyne.NewMenuItem("重命名 (RENAME)", func() { c.renameNodeDialog(nd) }))
	widget.NewPopUpMenu(fyne.NewMenu("", items...), c.win.Canvas()).ShowAtPosition(pos)
}

// confirmNodeAction renders the statement first, asks, then runs it.
func (c *Client) confirmNodeAction(nd *nodeData, action, newName string) {
	id := c.selectedOrLive()
	conn, err := c.openConn(id)
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	stmt, err := tableActionStatement(objectNames{
		dbType:     conn.Config.Type,
		objectType: nd.typ,
		database:   nd.db,
		schema:     defaultSchema(conn.Config.Type, nd.schema),
		table:      nd.name,
		newName:    newName,
	}, action)
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	dialog.ShowConfirm("确认执行", "将执行：\n\n"+stmt+"\n\n该操作不可恢复。", func(ok bool) {
		if !ok {
			return
		}
		run, err := c.runTableAction(id, nd.db, nd.schema, nd.name, action, newName)
		if err != nil {
			dialog.ShowError(err, c.win)
			return
		}
		c.setStatus("已执行：" + run)
		c.refreshSchema()
	}, c.win)
}

// renameNodeDialog asks for the new name, then runs the rename flow.
func (c *Client) renameNodeDialog(nd *nodeData) {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("新的名称（仅限标识符字符）")
	var d *dialog.CustomDialog
	form := widget.NewForm(widget.NewFormItem("重命名 "+nd.name, entry))
	form.OnSubmit = func() {
		d.Hide()
		c.confirmNodeAction(nd, actionRename, entry.Text)
	}
	form.SubmitText = "下一步"
	d = dialog.NewCustom("重命名", "取消", form, c.win)
	d.Resize(fyne.NewSize(380, 150))
	d.Show()
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

// onSchemaTableTapped fills the editor with a SELECT preview for the table.
func (c *Client) onSchemaTableTapped(uid string) {
	nd, ok := c.treeNodes[uid]
	if !ok || nd.kind != "table" {
		return
	}
	c.selectedNode = uid
	c.currentID = c.selectedOrLive()
	c.currentDB = nd.db
	c.queryEntry.setTextSilently(c.selectSQL(nd))
	c.dbSelRefresh()
	c.rebuildSchemaRows()
	c.focusEditor() // the next keystrokes should edit the generated SELECT
}

// selectedOrLive returns a usable connection id, preferring user selection.
func (c *Client) selectedOrLive() string {
	if c.currentID != "" {
		return c.currentID
	}
	if cfg, ok := c.selectedConn(); ok {
		return cfg.ID
	}
	return ""
}

// selectSQL builds a preview SELECT for a table node.
func (c *Client) selectSQL(n *nodeData) string {
	conn, err := c.openConn(c.currentID)
	if err != nil {
		return "SELECT * FROM " + plainIdent(n.name)
	}
	typ := conn.Config.Type

	schema := n.schema
	if schema == "" {
		schema = resolveSchema(typ, n.db, schema)
	}
	var parts []string
	switch typ {
	case dbTypeMySQL:
		parts = []string{n.db, n.name}
	case dbTypePostgres, dbTypeSQLServer:
		parts = []string{schema, n.name}
	default:
		parts = []string{n.name}
	}
	q, e := qualifiedName(typ, parts...)
	if e != nil {
		return "SELECT * FROM " + plainIdent(n.name)
	}
	return "SELECT * FROM " + q
}

// plainIdent returns an unquoted identifier fallback for SELECT previews.
func plainIdent(name string) string {
	if strings.ContainsAny(name, " \t\"") {
		return fmt.Sprintf("[%s]", name)
	}
	return name
}

// refreshSchema reloads the top level (databases); deeper levels load on
// demand when their row is tapped.
func (c *Client) refreshSchema() {
	c.mu.Lock()
	c.treeNodes = map[string]*nodeData{treeRootUID: {kind: "root", label: "数据库"}}
	c.mu.Unlock()
	c.expanded = make(map[string]bool)
	c.selectedNode = ""

	id := c.selectedOrLive()
	if id == "" || !c.connectionStatus(id) {
		c.mu.Lock()
		c.treeNodes[treeRootUID].label = "未连接"
		c.mu.Unlock()
		c.rebuildSchemaRows()
		return
	}

	c.setStatus("加载数据库 ...")
	dbs, err := c.databases(id)
	if err != nil {
		c.mu.Lock()
		c.treeNodes[treeRootUID].label = "加载失败：" + err.Error()
		c.mu.Unlock()
		c.rebuildSchemaRows()
		return
	}

	c.mu.Lock()
	root := c.treeNodes[treeRootUID]
	for _, db := range dbs {
		dbUID := "db:" + db
		c.treeNodes[dbUID] = &nodeData{kind: "db", label: db, db: db, uid: dbUID}
		root.children = append(root.children, dbUID)
	}
	root.label = fmt.Sprintf("数据库 (%d)", len(dbs))
	c.mu.Unlock()

	// Open the connection's own database right away, all the way down to its
	// tables — the reference navigator shows them the moment you connect.
	if cfg, ok := c.liveConfig(id); ok && cfg.Database != "" {
		if uid := "db:" + cfg.Database; c.treeNodes[uid] != nil {
			c.expanded[uid] = true
			dbNode := c.treeNodes[uid]
			c.loadChildren(dbNode)
			for _, ch := range dbNode.children {
				if s := c.treeNodes[ch]; s != nil && s.kind == "schema" {
					c.expanded[s.uid] = true
					c.loadChildren(s) // tables of this schema
				}
			}
		}
	}

	c.rebuildSchemaRows()
	c.setStatus("结构已加载")
}

// loadChildren lazily populates a node's children. Database nodes expand to
// schemas (PostgreSQL/SQL Server) or directly to tables; schema nodes expand
// to tables. Table nodes are leaves.
// loadChildren loads a node's children from the server. The catalog query
// runs off the UI thread; the tree mutation and re-render come back on the
// main goroutine, so expanding the navigator never freezes the window.
func (c *Client) loadChildren(nd *nodeData) {
	if nd.loaded {
		return
	}
	nd.loaded = true
	nd.loading = true
	id := c.selectedOrLive()
	if id == "" {
		nd.loading = false
		return
	}
	fetch := func() (schemas []string, tables []TableInfo, err error) {
		switch nd.kind {
		case "db":
			schemas, err = c.schemas(id, nd.db)
			if err == nil && len(schemas) == 0 {
				tables, err = c.tables(id, nd.db, "")
			}
		case "schema":
			tables, err = c.tables(id, nd.db, nd.schema)
		}
		return
	}
	if c.asyncRun {
		go func() {
			schemas, tables, err := fetch()
			c.dispatch(func() { c.applyChildren(nd, schemas, tables, err) })
		}()
		return
	}
	schemas, tables, err := fetch()
	c.applyChildren(nd, schemas, tables, err)
}

// applyChildren attaches fetched children to a node and re-renders the
// navigator. UI-thread only.
func (c *Client) applyChildren(nd *nodeData, schemas []string, tables []TableInfo, err error) {
	nd.loading = false
	switch {
	case err != nil:
		c.addErrorNode(nd, err)
	case len(schemas) > 0:
		c.attachSchemas(nd, schemas)
		// An expanded database also loads its schemas' tables so the tree
		// fills in like the reference navigator.
		for _, ch := range nd.children {
			if s := c.treeNodes[ch]; s != nil && s.kind == "schema" {
				c.expanded[s.uid] = true
				c.loadChildren(s)
			}
		}
	default:
		c.attachTables(nd, tables)
	}
	if c.expanded[nd.uid] {
		c.rebuildSchemaRows()
	}
}

// attachSchemas turns fetched schema names into child nodes.
func (c *Client) attachSchemas(nd *nodeData, schemas []string) {
	for _, sch := range schemas {
		schUID := nd.uid + "|s:" + sch
		c.mu.Lock()
		c.treeNodes[schUID] = &nodeData{kind: "schema", label: sch, db: nd.db, schema: sch, uid: schUID}
		nd.children = append(nd.children, schUID)
		c.mu.Unlock()
	}
}

// attachTables turns fetched table rows into child nodes; a result with no
// rows gets a diagnostic node instead of a silent blank expansion.
func (c *Client) attachTables(nd *nodeData, tables []TableInfo) {
	if len(tables) == 0 {
		uid := nd.uid + "|empty"
		c.mu.Lock()
		c.treeNodes[uid] = &nodeData{
			kind:   "empty",
			label:  "（没有可见的表：登录可能无权限，或该库为空）",
			uid:    uid,
			parent: nd.uid,
		}
		nd.children = []string{uid}
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range tables {
		tSchema := t.Schema
		if tSchema == "" {
			tSchema = nd.schema
		}
		label := fmt.Sprintf("%s [%s]", t.Name, t.Type)
		if tSchema != nd.schema {
			// The row came from the cross-schema fallback: show where it lives.
			label = fmt.Sprintf("%s.%s [%s]", tSchema, t.Name, t.Type)
		}
		tUID := nd.uid + "|t:" + tSchema + "." + t.Name
		c.treeNodes[tUID] = &nodeData{
			kind:   "table",
			label:  label,
			db:     nd.db,
			schema: tSchema,
			name:   t.Name,
			uid:    tUID,
			typ:    t.Type,
		}
		nd.children = append(nd.children, tUID)
	}
}

// addErrorNode attaches a red, retryable error row under nd, replacing any
// children (a failed load produces none anyway).
func (c *Client) addErrorNode(nd *nodeData, err error) {
	uid := nd.uid + "|error"
	c.mu.Lock()
	c.treeNodes[uid] = &nodeData{
		kind:   "error",
		label:  "⚠ 加载失败：" + err.Error(),
		uid:    uid,
		parent: nd.uid,
	}
	nd.children = []string{uid}
	c.mu.Unlock()
	c.setStatus("加载失败：" + err.Error())
}

// retrySchemaNode clears a failed load and expands the parent again.
func (c *Client) retrySchemaNode(parentUID string) {
	parent := c.treeNodes[parentUID]
	if parent == nil {
		return
	}
	parent.loaded = false
	parent.children = nil
	delete(c.expanded, parentUID)
	c.toggleSchemaNode(parentUID)
}
