package main

// Left sidebar of the workspace: Items/Queries/History switcher, search box,
// Tables/Views tree fed from the live connection, and the database selector
// at the bottom.

import (
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type sidebarMode int

const (
	sidebarItems sidebarMode = iota
	sidebarQueries
	sidebarHistory
)

type sidebar struct {
	widget.BaseWidget
	mode       sidebarMode
	onSelect   func(string) // table/view picked
	onNewQuery func()
	onDBPick   func(string)                // database chosen in the bottom selector
	onNewDB    func()                      // "+" next to the bottom selector
	onItemMenu func(string, fyne.Position) // right-click on an item
	// saved-query panel (查询 tab)
	onQueryRun  func(q SavedQuery) // open + run a saved query
	onQueryOpen func(q SavedQuery) // open a saved query in the editor
	onQuerySave func()             // "save current query" button
	onQueryMenu func(q SavedQuery, pos fyne.Position)
	queriesPane *fyne.Container // rows of the 查询 tab (rebuilt by refreshQueries)
	tree        *widget.Tree
	search      *widget.Entry
	dbSelect    *widget.Select
	body        *sidebarBody // pane switcher; refreshed on every state change
	suppress    bool         // guard Select.OnChanged during programmatic changes
	objects     []TableInfo  // full inventory from the session
	filtered    []TableInfo  // after search filter
	database    string
	history     []string
	loaded      bool
	noDB        bool            // true until a database is selected (shows the prompt)
	pinned      map[string]bool // table names pinned to the top of the tree
}

func newSidebar(onSelect func(string), onNewQuery func()) *sidebar {
	s := &sidebar{onSelect: onSelect, onNewQuery: onNewQuery}
	s.queriesPane = container.NewStack()
	s.body = &sidebarBody{s: s}
	s.ExtendBaseWidget(s)
	s.refreshQueries() // build the initial (likely empty) 查询 tab
	return s
}

// refreshQueries rebuilds the 查询 tab rows from the persisted saved queries.
func (s *sidebar) refreshQueries() {
	if s.queriesPane == nil {
		return
	}
	qs := sortedSavedQueries()
	list := container.NewVBox()
	if len(qs) == 0 {
		hint := widget.NewLabelWithStyle("暂无保存的查询。\n点“保存当前查询”把编辑器内容存下来。",
			fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
		hint.Wrapping = fyne.TextWrapWord
		list.Objects = append(list.Objects, container.NewPadded(hint))
	}
	for _, q := range qs {
		list.Objects = append(list.Objects, newQueryRow(q, s))
	}
	var top fyne.CanvasObject
	if s.onQuerySave != nil {
		saveBtn := newFlatButton("保存当前查询")
		saveBtn.onTap = s.onQuerySave
		top = saveBtn
	}
	pane := container.NewBorder(top, nil, nil, nil, container.NewScroll(container.NewPadded(list)))
	s.queriesPane.Objects = []fyne.CanvasObject{pane}
	s.queriesPane.Refresh()
}

// treeID constants
const (
	nodeFunctions = "::functions"
	nodeTables    = "::tables"
	nodeViews     = "::views"
)

// branchLabel maps a tree group ID to its display name.
func branchLabel(id string) string {
	switch id {
	case nodeFunctions:
		return "函数"
	case nodeTables:
		return "表格"
	case nodeViews:
		return "视图"
	}
	return id
}

// setInventory swaps the object list from the session and rebuilds the tree.
func (s *sidebar) setInventory(objs []TableInfo, database string) {
	s.objects = objs
	s.database = database
	s.loaded = true
	s.noDB = false
	if s.search != nil {
		s.applyFilter()
	}
	// The tree's child lists are read dynamically from s.filtered, so a
	// refresh is required for the just-loaded objects to show up.
	if s.tree != nil {
		s.tree.Refresh()
		s.tree.OpenAllBranches()
	}
	s.setDatabase(database)
	s.Refresh()
}

// appendHistory records one executed statement for the History pane.
func (s *sidebar) appendHistory(sqlText string) {
	s.history = append(s.history, sqlText)
	s.Refresh()
}

// historyItems returns a copy of the executed-statement history, used by the
// quick-open palette.
func (s *sidebar) historyItems() []string {
	out := make([]string, len(s.history))
	copy(out, s.history)
	return out
}

// setNoDatabase switches the tree area to the "no database selected" prompt
// shown before the user picks a database (TablePlus: Ctrl+K).
func (s *sidebar) setNoDatabase() {
	s.noDB = true
	s.loaded = false
	s.objects = nil
	s.filtered = nil
	s.database = ""
	if s.dbSelect != nil {
		s.suppress = true
		s.dbSelect.ClearSelected()
		s.suppress = false
	}
	s.Refresh()
}

func (s *sidebar) hasViews() bool {
	for _, o := range s.filtered {
		if o.Kind == "view" {
			return true
		}
	}
	return false
}

func (s *sidebar) hasTables() bool {
	for _, o := range s.filtered {
		if o.Kind == "table" {
			return true
		}
	}
	return false
}

func (s *sidebar) hasFunctions() bool {
	for _, o := range s.filtered {
		if o.Kind == "function" {
			return true
		}
	}
	return false
}

// isPinned reports whether the item is pinned to the top of the tree.
func (s *sidebar) isPinned(name string) bool { return s.pinned[name] }

// togglePin flips the pin state for a tree item (TablePlus "Pin to top").
func (s *sidebar) togglePin(name string) {
	if s.pinned == nil {
		s.pinned = map[string]bool{}
	}
	if s.pinned[name] {
		delete(s.pinned, name)
	} else {
		s.pinned[name] = true
	}
	s.applyFilter()
	s.tree.Refresh()
}

func (s *sidebar) applyFilter() {
	q := ""
	if s.search != nil {
		q = strings.ToLower(strings.TrimSpace(s.search.Text))
	}
	s.filtered = s.filtered[:0]
	for _, o := range s.objects {
		if q == "" || strings.Contains(strings.ToLower(o.Name), q) {
			s.filtered = append(s.filtered, o)
		}
	}
	sort.Slice(s.filtered, func(i, j int) bool {
		// Pinned items float to the very top of their kind group.
		if p, q := s.pinned[s.filtered[i].Name], s.pinned[s.filtered[j].Name]; p != q {
			return p
		}
		order := map[string]int{"function": 0, "table": 1, "view": 2}
		if oi, oj := order[s.filtered[i].Kind], order[s.filtered[j].Kind]; oi != oj {
			return oi < oj
		}
		return s.filtered[i].Name < s.filtered[j].Name
	})
}

func (s *sidebar) CreateRenderer() fyne.WidgetRenderer {
	// mode switcher: 项目 | 查询 | 历史记录
	itemsBtn := newSideModeButton("项目", func() { s.setMode(sidebarItems) })
	queriesBtn := newSideModeButton("查询", func() {
		s.setMode(sidebarQueries)
		if s.onNewQuery != nil {
			s.onNewQuery()
		}
	})
	historyBtn := newSideModeButton("历史记录", func() { s.setMode(sidebarHistory) })
	modeRow := container.NewGridWithColumns(3, itemsBtn, queriesBtn, historyBtn)

	s.search = widget.NewEntry()
	s.search.PlaceHolder = "搜索表..."
	s.search.OnChanged = func(string) {
		if s.loaded {
			s.applyFilter()
			s.tree.Refresh()
		}
	}
	searchRow := container.NewHBox(widget.NewIcon(theme.SearchIcon()), s.search)

	// tree of 函数 / 表格 / 视图
	// NB: the root id ("") must also report as a branch — Fyne's Tree only
	// walks a node's children when IsBranch is true for that node, so an
	// empty-root branch makes the whole tree render empty.
	isBranch := func(id string) bool {
		return id == "" || id == nodeFunctions || id == nodeTables || id == nodeViews
	}
	childUIDs := func(id string) []string {
		switch id {
		case "":
			var out []string
			out = append(out, nodeFunctions, nodeTables) // always shown, like TablePlus
			if s.hasViews() {
				out = append(out, nodeViews)
			}
			return out
		case nodeFunctions, nodeTables, nodeViews:
			kind := "table"
			if id == nodeViews {
				kind = "view"
			} else if id == nodeFunctions {
				kind = "function"
			}
			var out []string
			for _, o := range s.filtered {
				if o.Kind == kind {
					out = append(out, o.Name)
				}
			}
			return out
		}
		return nil
	}
	s.tree = widget.NewTree(
		childUIDs,
		isBranch,
		func(branch bool) fyne.CanvasObject {
			it := newTreeItem()
			it.onSecondary = s.onItemMenu
			// treeItem is the deepest object under the pointer and implements
			// SecondaryTappable, so Fyne's hit test stops here. Without a
			// primary Tapped handler the click would be swallowed and never
			// reach the tree node. Forward it to the tree's selection.
			it.onTap = func(id string) {
				if s.tree != nil {
					s.tree.Select(id)
				}
			}
			return it
		},
		func(id string, branch bool, o fyne.CanvasObject) {
			it, ok := o.(*treeItem)
			if !ok {
				return
			}
			it.id = id
			if isBranch(id) {
				it.icon.Resource = theme.FolderOpenIcon()
				it.label.SetText(branchLabel(id))
			} else {
				it.icon.Resource = theme.StorageIcon()
				it.label.SetText(id)
			}
			it.icon.Refresh()
		},
	)
	s.tree.OnSelected = func(id string) {
		if !isBranch(id) && s.onSelect != nil {
			s.onSelect(id)
		}
	}
	s.tree.OpenAllBranches()

	// Bottom: schema/database selector + "new database" (TablePlus style).
	s.dbSelect = widget.NewSelect(nil, func(choice string) {
		if s.suppress || choice == "" {
			return
		}
		if s.onDBPick != nil {
			s.onDBPick(choice)
		}
	})
	newDB := newToolButton(theme.ContentAddIcon(), func() {
		if s.onNewDB != nil {
			s.onNewDB()
		}
	})
	schemaRow := container.NewBorder(nil, nil, nil, newDB, s.dbSelect)

	body := s.body

	root := container.NewBorder(
		container.NewVBox(modeRow, searchRow, widget.NewSeparator()),
		schemaRow,
		nil, nil,
		container.NewStack(body),
	)
	return widget.NewSimpleRenderer(root)
}

// Refresh repaints the sidebar and re-evaluates which pane (tree/queries/
// history/no-database) is visible. Without this the sidebarBody stack keeps
// every pane shown at once, hiding the tree behind the prompt labels.
func (s *sidebar) Refresh() {
	s.BaseWidget.Refresh()
	if s.body != nil {
		s.body.Refresh()
	}
}

// setDatabase marks the given database as the current selection.
func (s *sidebar) setDatabase(name string) {
	s.database = name
	if s.dbSelect == nil {
		return
	}
	s.suppress = true
	if name != "" {
		s.dbSelect.SetSelected(name)
	} else {
		s.dbSelect.ClearSelected()
	}
	s.suppress = false
}

// setDatabases fills the bottom selector with the server's databases and
// selects the current one.
func (s *sidebar) setDatabases(list []string, current string) {
	if s.dbSelect == nil {
		return
	}
	s.suppress = true
	s.dbSelect.Options = list
	s.dbSelect.Refresh()
	if current != "" {
		s.dbSelect.SetSelected(current)
	}
	s.suppress = false
}

func (s *sidebar) setMode(m sidebarMode) {
	s.mode = m
	if m == sidebarQueries {
		s.refreshQueries()
	}
	s.Refresh()
}

// sidebarBody switches between the tree, the queries list and history text.
type sidebarBody struct {
	widget.BaseWidget
	s     *sidebar
	stack *fyne.Container
	treeWrap,
	queries,
	history,
	noDB fyne.CanvasObject
}

func (b *sidebarBody) CreateRenderer() fyne.WidgetRenderer {
	history := widget.NewLabelWithStyle("-- 暂无查询", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	history.Wrapping = fyne.TextWrapWord
	noDB := widget.NewLabelWithStyle("未选择数据库，请按Ctrl K选择数据库",
		fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
	noDB.Wrapping = fyne.TextWrapWord
	b.queries = b.s.queriesPane // live saved-query panel (rebuilt on demand)
	b.history = history
	b.noDB = noDB
	b.treeWrap = container.NewPadded(b.s.tree)
	b.stack = container.NewStack(b.treeWrap, b.queries, b.history, container.NewCenter(noDB))
	b.applyPane() // establish the initially visible pane
	return widget.NewSimpleRenderer(b.stack)
}

// applyPane shows only the pane for the current mode (or the no-database
// prompt), hiding the rest of the stack.
func (b *sidebarBody) applyPane() {
	if b.stack == nil {
		return
	}
	idx := 0
	switch b.s.mode {
	case sidebarQueries:
		idx = 1
	case sidebarHistory:
		idx = 2
	}
	if b.s.noDB {
		idx = 3 // "未选择数据库" prompt
	}
	for i, o := range b.stack.Objects {
		if i == idx {
			o.Show()
		} else {
			o.Hide()
		}
	}
}

func (b *sidebarBody) Refresh() {
	if b.stack == nil {
		return
	}
	// keep history text current
	if hl, ok := b.history.(*widget.Label); ok {
		if len(b.s.history) == 0 {
			hl.SetText("-- 暂无查询")
		} else {
			out := ""
			start := 0
			if n := len(b.s.history); n > 10 {
				start = n - 10
			}
			for i := start; i < len(b.s.history); i++ {
				out += b.s.history[i] + ";\n\n"
			}
			hl.SetText(out)
		}
	}
	b.applyPane()
	b.stack.Refresh()
	b.BaseWidget.Refresh()
}

// treeItem is one row of the Items tree. It renders an icon + label like
// Fyne's default node, and implements fyne.Tappable / fyne.SecondaryTappable
// so both a left click (open/select) and a right click (context menu) work.
type treeItem struct {
	widget.BaseWidget
	id          string
	icon        *canvas.Image
	label       *widget.Label
	onTap       func(id string)
	onSecondary func(id string, pos fyne.Position)
}

func newTreeItem() *treeItem {
	it := &treeItem{
		icon:  canvas.NewImageFromResource(theme.StorageIcon()),
		label: widget.NewLabel("template"),
	}
	it.icon.SetMinSize(fyne.NewSize(16, 16))
	it.label.Truncation = fyne.TextTruncateEllipsis
	it.ExtendBaseWidget(it)
	return it
}

func (it *treeItem) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewBorder(nil, nil, it.icon, nil, it.label))
}

func (it *treeItem) Tapped(*fyne.PointEvent) {
	if it.onTap != nil && it.id != "" {
		it.onTap(it.id)
	}
}

func (it *treeItem) TappedSecondary(e *fyne.PointEvent) {
	if it.onSecondary != nil && it.id != "" {
		it.onSecondary(it.id, e.AbsolutePosition)
	}
}

// sideModeButton is a flat text button highlighting the active sidebar mode.
type sideModeButton struct {
	widget.BaseWidget
	label  string
	active bool
	onTap  func()
}

func newSideModeButton(label string, onTap func()) *sideModeButton {
	b := &sideModeButton{label: label, onTap: onTap}
	b.ExtendBaseWidget(b)
	return b
}

func (b *sideModeButton) setActive(a bool) {
	if b.active != a {
		b.active = a
		b.Refresh()
	}
}

func (b *sideModeButton) Tapped(*fyne.PointEvent) {
	if b.onTap != nil {
		b.onTap()
	}
}
func (b *sideModeButton) Cursor() desktop.Cursor { return desktop.DefaultCursor }

func (b *sideModeButton) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(pal().hover)
	bg.CornerRadius = 6
	text := canvas.NewText(b.label, pal().activeTabFG)
	text.TextSize = 13
	text.Alignment = fyne.TextAlignCenter
	return &sideModeBtnRenderer{b: b, bg: bg, text: text}
}

type sideModeBtnRenderer struct {
	b    *sideModeButton
	bg   *canvas.Rectangle
	text *canvas.Text
}

func (r *sideModeBtnRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.text}
}

func (r *sideModeBtnRenderer) MinSize() fyne.Size { return fyne.NewSize(64, 26) }

func (r *sideModeBtnRenderer) Destroy() {}

func (r *sideModeBtnRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	t := r.text.MinSize()
	r.text.Move(fyne.NewPos((size.Width-t.Width)/2, (size.Height-t.Height)/2))
}

func (r *sideModeBtnRenderer) Refresh() {
	if r.b.active {
		r.bg.FillColor = pal().segmentBG
		r.text.TextStyle.Bold = true
	} else {
		r.bg.FillColor = colorTransparent
		r.text.TextStyle.Bold = false
	}
	r.text.Color = pal().activeTabFG
	r.bg.Refresh()
	r.text.Refresh()
}

// ---------------------------------------------------------------------------
// queryRow: one saved-query entry in the 查询 tab. Tap to run, right-click
// for a context menu (run/copy/delete, provided by the sidebar owner).

type queryRow struct {
	widget.BaseWidget
	q    SavedQuery
	s    *sidebar
	icon *canvas.Image
	name *canvas.Text
	kw   *canvas.Text
}

func newQueryRow(q SavedQuery, s *sidebar) *queryRow {
	r := &queryRow{q: q, s: s}
	r.icon = canvas.NewImageFromResource(theme.DocumentIcon())
	r.icon.SetMinSize(fyne.NewSize(16, 16))
	r.name = canvas.NewText(q.Name, pal().foreground)
	r.name.TextSize = 13
	r.name.TextStyle.Bold = true
	r.kw = canvas.NewText("", pal().metaKeyFG)
	r.kw.TextSize = 11
	if len(q.Keywords) > 0 {
		r.kw.Text = "关键字: " + strings.Join(q.Keywords, ", ")
	}
	r.ExtendBaseWidget(r)
	return r
}

func (r *queryRow) Tapped(*fyne.PointEvent) {
	if r.s.onQueryRun != nil {
		r.s.onQueryRun(r.q)
	}
}

func (r *queryRow) TappedSecondary(e *fyne.PointEvent) {
	if r.s.onQueryMenu != nil {
		r.s.onQueryMenu(r.q, e.AbsolutePosition)
	}
}

func (r *queryRow) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (r *queryRow) CreateRenderer() fyne.WidgetRenderer {
	lead := container.NewCenter(r.icon)
	texts := container.NewVBox(r.name, r.kw)
	row := container.NewBorder(lead, nil, nil, nil, texts)
	return widget.NewSimpleRenderer(container.NewPadded(row))
}
