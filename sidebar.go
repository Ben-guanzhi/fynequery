package main

// Left sidebar of the workspace: Items/Queries/History switcher, search box,
// Tables/Views tree fed from the live connection, and the database selector
// at the bottom.

import (
	"fynequery/icons"
	"image/color"
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
	onSearch   func(string)                // search text changed (KV: server-side filter)
	onDBPick   func(string)                // database chosen in the bottom selector
	onNewDB    func()                      // "+" next to the bottom selector
	onItemMenu func(string, fyne.Position) // right-click on an item
	onMoreKeys func()                      // "load more keys" row (KV, paged inventory)
	// saved-query panel (查询 tab)
	onQueryRun  func(q SavedQuery) // open + run a saved query
	onQueryOpen func(q SavedQuery) // open a saved query in the editor
	onQuerySave func()             // "save current query" button
	onQueryMenu func(q SavedQuery, pos fyne.Position)
	queriesPane *fyne.Container // rows of the 查询 tab (rebuilt by refreshQueries)
	tree        *widget.Tree
	search      *widget.Entry
	searchWrap  fyne.CanvasObject
	dbSelect    *widget.Select
	schemaWrap  fyne.CanvasObject
	modeBtns    []*sideModeButton
	body        *sidebarBody // pane switcher; refreshed on every state change
	suppress    bool         // guard Select.OnChanged during programmatic changes
	objects     []TableInfo  // full inventory from the session
	filtered    []TableInfo  // after search filter
	database    string
	selID       string // currently selected tree node id (drives white-on-blue rows)
	history     []string
	loaded      bool
	loadErr     string            // last inventory load error ("" = none)
	noDB        bool              // true until a database is selected (shows the prompt)
	kv          bool              // key/value store: inventory entries are keys, not tables
	moreKeys    bool              // KV: the session has a further key page
	pinned      map[string]bool   // table names pinned to the top of the tree
	searchClear fyne.CanvasObject // "x" clears the search box (shown when non-empty)
	// suppressSearch silences OnChanged while the box is reset programmatically.
	suppressSearch bool
}

func newSidebar(onSelect func(string), onNewQuery func()) *sidebar {
	s := &sidebar{onSelect: onSelect, onNewQuery: onNewQuery}
	s.queriesPane = container.NewStack()
	s.body = &sidebarBody{s: s}
	// Without ExtendBaseWidget the body's super() stays nil, so BaseWidget.Resize
	// returns before laying out the inner stack and the tree never widens with
	// the sidebar (table names truncate against free space).
	s.body.ExtendBaseWidget(s.body)
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
	// nodeMore is a KV-only pseudo row: its id starts with "::" so it can never
	// collide with a key name and the item menu ignores it.
	nodeMore = "::more"
)

// branchLabel maps a tree group ID to its display name.
func (s *sidebar) branchLabel(id string) string {
	switch id {
	case nodeFunctions:
		return "函数"
	case nodeTables:
		if s.kv {
			return "键"
		}
		return "表格"
	case nodeViews:
		return "视图"
	case nodeMore:
		return "加载更多键…"
	}
	return id
}

// rootGroups lists the top-level tree groups. A key/value store has no
// functions and no views — every entry is a key.
func (s *sidebar) rootGroups() []string {
	var out []string
	if !s.kv {
		out = append(out, nodeFunctions)
	}
	out = append(out, nodeTables) // always shown, like TablePlus
	if s.hasViews() {
		out = append(out, nodeViews)
	}
	return out
}

// setInventory swaps the object list from the session and rebuilds the tree.
func (s *sidebar) setInventory(objs []TableInfo, database string) {
	s.objects = objs
	s.database = database
	s.loaded = true
	s.loadErr = ""
	s.noDB = false
	if s.searchWrap != nil {
		s.searchWrap.Show()
	}
	if s.schemaWrap != nil {
		s.schemaWrap.Show()
	}
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
	s.loadErr = ""
	s.objects = nil
	s.filtered = nil
	s.moreKeys = false
	s.database = ""
	if s.searchWrap != nil {
		s.searchWrap.Hide()
	}
	if s.schemaWrap != nil {
		s.schemaWrap.Hide()
	}
	if s.dbSelect != nil {
		s.suppress = true
		s.dbSelect.ClearSelected()
		s.suppress = false
	}
	s.Refresh()
}

// setLoadError records a failed inventory load so the tree area shows the
// error instead of spinning on "加载中…" forever.
func (s *sidebar) setLoadError(msg string) {
	s.loadErr = msg
	s.loaded = true
	s.noDB = false
	s.Refresh()
}

// resetSearch clears the search box without notifying the workspace. Used when
// the session is replaced and its server-side key filter is already gone.
func (s *sidebar) resetSearch() {
	if s.search == nil || s.search.Text == "" {
		return
	}
	s.suppressSearch = true
	s.search.SetText("")
	s.suppressSearch = false
	if s.searchClear != nil {
		s.searchClear.Hide()
	}
}

// treeStatusText returns the overlay text (and whether to show it) for the
// items tree in place of a bare empty group list: a load error, loading, an
// empty database, or a no-match search.
func (s *sidebar) treeStatusText() (string, bool) {
	if s.loadErr != "" {
		return "加载失败：" + s.loadErr, true
	}
	if !s.loaded {
		return "加载中…", true
	}
	q := ""
	if s.search != nil {
		q = strings.TrimSpace(s.search.Text)
	}
	if q != "" && len(s.filtered) == 0 {
		return "无匹配「" + q + "」", true
	}
	if len(s.objects) == 0 {
		if s.kv {
			return "此数据库暂无键", true
		}
		return "此数据库暂无表", true
	}
	return "", false
}

func (s *sidebar) hasViews() bool {
	for _, o := range s.filtered {
		if o.Kind == "view" {
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
	// A KV session filters keys on the server (the search text may be a glob),
	// so re-applying substring matching here would drop valid hits.
	if s.search != nil && !s.kv {
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

// childIDs lists the tree rows under id. KV inventories append a "load more"
// row while the session holds a further key page.
func (s *sidebar) childIDs(id string) []string {
	switch id {
	case "":
		return s.rootGroups()
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
		if id == nodeTables && s.kv && s.moreKeys {
			out = append(out, nodeMore)
		}
		return out
	}
	return nil
}

// itemSecondary routes a right-click to the item menu. Group headers and the
// KV "load more" pseudo row have "::" ids and are not objects.
func (s *sidebar) itemSecondary(id string, pos fyne.Position) {
	if s.onItemMenu == nil || id == "" || strings.HasPrefix(id, "::") {
		return
	}
	s.onItemMenu(id, pos)
}

// setMoreKeys shows or hides the KV "load more" row.
func (s *sidebar) setMoreKeys(more bool) {
	if s.moreKeys == more {
		return
	}
	s.moreKeys = more
	if s.tree != nil {
		s.tree.Refresh()
	}
	s.Refresh()
}

// leafIcon picks a light, type-specific glyph so tables, views and functions
// read apart at a glance without the heavy solid database cylinder.
func (s *sidebar) leafIcon(name string) fyne.Resource {
	for _, o := range s.filtered {
		if o.Name != name {
			continue
		}
		switch o.Kind {
		case "function":
			return icons.Function
		case "view":
			return icons.View
		}
		break
	}
	return icons.Table
}

func (s *sidebar) CreateRenderer() fyne.WidgetRenderer {
	// mode switcher: 项目 | 查询 | 历史记录, as left-aligned pills.
	itemsBtn := newSideModeButton("项目", func() { s.setMode(sidebarItems) })
	queriesBtn := newSideModeButton("查询", func() {
		s.setMode(sidebarQueries)
		if s.onNewQuery != nil {
			s.onNewQuery()
		}
	})
	historyBtn := newSideModeButton("历史记录", func() { s.setMode(sidebarHistory) })
	s.modeBtns = []*sideModeButton{itemsBtn, queriesBtn, historyBtn}
	for i, b := range s.modeBtns {
		b.setActive(i == int(s.mode))
	}
	modeRow := container.NewBorder(nil, nil, spacerW(16), nil,
		container.NewHBox(itemsBtn, spacerW(4), queriesBtn, spacerW(4), historyBtn))

	s.search = widget.NewEntry()
	s.search.PlaceHolder = "搜索表..."
	if s.kv {
		s.search.PlaceHolder = "搜索键（支持 * ? [）..."
	}
	sc := newToolButton(icons.Cancel, func() { s.search.SetText("") })
	sc.small = true
	s.searchClear = sc
	s.searchClear.Hide()
	s.search.OnChanged = func(text string) {
		if s.suppressSearch {
			return
		}
		if s.searchClear != nil {
			if s.search.Text != "" {
				s.searchClear.Show()
			} else {
				s.searchClear.Hide()
			}
		}
		if s.onSearch != nil {
			s.onSearch(text)
		}
		if s.loaded && !s.kv {
			s.applyFilter()
			s.tree.Refresh()
		}
		s.Refresh()
	}
	searchRow := container.NewBorder(nil, nil, widget.NewIcon(icons.Search), s.searchClear, s.search)
	s.searchWrap = container.NewPadded(searchRow)
	if !s.loaded {
		s.searchWrap.Hide()
	}

	// tree of 函数 / 表格 / 视图
	// NB: the root id ("") must also report as a branch — Fyne's Tree only
	// walks a node's children when IsBranch is true for that node, so an
	// empty-root branch makes the whole tree render empty.
	isBranch := func(id string) bool {
		return id == "" || id == nodeFunctions || id == nodeTables || id == nodeViews
	}
	childUIDs := s.childIDs
	s.tree = widget.NewTree(
		childUIDs,
		isBranch,
		func(branch bool) fyne.CanvasObject {
			it := newTreeItem()
			it.onSecondary = s.itemSecondary
			// A row paints white text when it carries the current selection.
			it.isSel = func() bool { return s.selID == it.id && it.id != "" }
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
				it.icon.Resource = icons.Folder
				it.setText(s.branchLabel(id))
			} else if id == nodeMore {
				it.icon.Resource = icons.Folder
				it.setText(s.branchLabel(id))
			} else {
				it.icon.Resource = s.leafIcon(id)
				it.setText(id)
			}
			it.icon.Refresh()
			it.Refresh()
		},
	)
	s.tree.OnSelected = func(id string) {
		if id == nodeMore {
			// A pseudo row, not a selectable object: fire the load and drop
			// the selection so it can be tapped again while paging.
			s.selID = ""
			s.tree.UnselectAll()
			s.tree.Refresh()
			if s.onMoreKeys != nil {
				s.onMoreKeys()
			}
			return
		}
		s.selID = id
		s.tree.Refresh() // repaint so the row's white-on-blue text follows
		if !isBranch(id) && s.onSelect != nil {
			s.onSelect(id)
		}
	}
	s.tree.OpenAllBranches()

	// Bottom: schema/database selector + "new database". Hidden until the
	// server's database list arrives so the no-database state stays clean.
	s.dbSelect = widget.NewSelect(nil, func(choice string) {
		if s.suppress || choice == "" {
			return
		}
		if s.onDBPick != nil {
			s.onDBPick(choice)
		}
	})
	s.dbSelect.PlaceHolder = "选择数据库"
	newDB := newToolButton(theme.ContentAddIcon(), func() {
		if s.onNewDB != nil {
			s.onNewDB()
		}
	})
	schemaRow := container.NewBorder(nil, nil, nil, newDB, s.dbSelect)
	s.schemaWrap = newPanelBackground(container.NewPadded(schemaRow),
		func() color.Color { return pal().sidebarBG })
	s.schemaWrap.Hide()

	body := s.body

	root := container.NewBorder(
		container.NewVBox(container.NewPadded(modeRow), s.searchWrap),
		s.schemaWrap,
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
	if s.schemaWrap != nil {
		if len(list) > 0 && !s.noDB {
			s.schemaWrap.Show()
		} else {
			s.schemaWrap.Hide()
		}
	}
}

func (s *sidebar) setMode(m sidebarMode) {
	s.mode = m
	for i, b := range s.modeBtns {
		b.setActive(i == int(m))
	}
	if m == sidebarQueries {
		s.refreshQueries()
	}
	s.Refresh()
}

// sidebarBody switches between the tree, the queries list and history text.
type sidebarBody struct {
	widget.BaseWidget
	s      *sidebar
	stack  *fyne.Container
	status *widget.Label
	treeWrap,
	queries,
	history,
	noDB fyne.CanvasObject
}

func (b *sidebarBody) CreateRenderer() fyne.WidgetRenderer {
	history := widget.NewLabelWithStyle("-- 暂无查询", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	history.Wrapping = fyne.TextWrapWord
	noDB := widget.NewLabelWithStyle("未选择数据库，请按Ctrl K选择数据库",
		fyne.TextAlignCenter, fyne.TextStyle{})
	noDB.Wrapping = fyne.TextWrapWord
	noDB.Importance = widget.LowImportance
	status := widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{})
	status.Wrapping = fyne.TextWrapWord
	status.Importance = widget.LowImportance
	b.queries = b.s.queriesPane // live saved-query panel (rebuilt on demand)
	b.history = history
	b.noDB = noDB
	b.status = status
	b.treeWrap = container.NewPadded(b.s.tree)
	b.stack = container.NewStack(b.treeWrap, b.queries, b.history, container.NewCenter(noDB), container.NewCenter(status))
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
	} else if idx == 0 {
		// Items (tree) mode: surface loading / error / empty / no-match states
		// instead of a bare, ambiguous empty group list.
		if txt, show := b.s.treeStatusText(); show {
			if b.status != nil {
				b.status.SetText(txt)
			}
			idx = 4
		}
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
// The label is a canvas.Text (not widget.Label) so the row can paint white
// text over the blue selection highlight; clipping to the row width replaces
// widget.Label's ellipsis truncation.
type treeItem struct {
	widget.BaseWidget
	id          string
	text        string // raw (unclipped) label
	icon        *canvas.Image
	label       *canvas.Text
	onTap       func(id string)
	onSecondary func(id string, pos fyne.Position)
	isSel       func() bool // whether this row carries the tree selection
}

func newTreeItem() *treeItem {
	it := &treeItem{
		icon:  canvas.NewImageFromResource(theme.StorageIcon()),
		label: canvas.NewText("template", pal().foreground),
	}
	it.icon.SetMinSize(fyne.NewSize(16, 16))
	// Border stretches the left slot to the full row height; Contain keeps the
	// glyph square and centred instead of smearing it into a vertical blob.
	it.icon.FillMode = canvas.ImageFillContain
	it.icon.Translucency = 0.35 // fade the glyph so it reads as a hint, not a blob
	it.label.TextSize = 13
	it.ExtendBaseWidget(it)
	return it
}

// setText stores the raw label and repaints (clipping happens in Layout).
func (it *treeItem) setText(s string) {
	if it.text != s {
		it.text = s
		it.Refresh()
	}
}

func (it *treeItem) CreateRenderer() fyne.WidgetRenderer {
	return &treeItemRenderer{it: it}
}

type treeItemRenderer struct {
	it *treeItem
}

func (r *treeItemRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.it.icon, r.it.label}
}

// MinSize stays narrow on purpose: rows clip with an ellipsis instead of
// forcing the sidebar wider than the user dragged it.
func (r *treeItemRenderer) MinSize() fyne.Size {
	return fyne.NewSize(62, 24)
}

func (r *treeItemRenderer) Layout(size fyne.Size) {
	icSz := float32(16)
	r.it.icon.Move(fyne.NewPos(0, (size.Height-icSz)/2))
	r.it.icon.Resize(fyne.NewSize(icSz, icSz))
	x := icSz + 6
	w := size.Width - x - 4
	if w < 0 {
		w = 0
	}
	r.it.label.Text = fitCellText(r.it.text, w, r.it.label.TextSize, r.it.label.TextStyle)
	t := r.it.label.MinSize()
	r.it.label.Move(fyne.NewPos(x, (size.Height-t.Height)/2))
}

func (r *treeItemRenderer) Destroy() {}

func (r *treeItemRenderer) Refresh() {
	p := pal()
	if r.it.isSel != nil && r.it.isSel() {
		r.it.label.Color = p.selFG // white over the blue selection
	} else {
		r.it.label.Color = p.foreground
	}
	r.it.label.Refresh()
	r.it.icon.Refresh()
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
	p := pal()
	fill := colorTransparent
	fg := p.metaKeyFG
	if b.active {
		fill = p.segmentBG
		fg = p.activeTabFG
	}
	bg := canvas.NewRectangle(fill)
	bg.CornerRadius = 6
	text := canvas.NewText(b.label, fg)
	text.TextSize = 13
	text.TextStyle.Bold = b.active
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

func (r *sideModeBtnRenderer) MinSize() fyne.Size {
	// Reserve the BOLD text width even when inactive. The active state toggles
	// TextStyle.Bold (see Refresh); without reserving the wider bold width, the
	// button's MinSize would grow on activation and re-lay-out the row, making
	// the sibling tab labels visibly shift on every switch.
	w := fyne.MeasureText(r.text.Text, r.text.TextSize, fyne.TextStyle{Bold: true}).Width
	return fyne.NewSize(w+20, 24)
}

func (r *sideModeBtnRenderer) Destroy() {}

func (r *sideModeBtnRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	t := r.text.MinSize()
	r.text.Move(fyne.NewPos((size.Width-t.Width)/2, (size.Height-t.Height)/2))
}

func (r *sideModeBtnRenderer) Refresh() {
	p := pal()
	if r.b.active {
		r.bg.FillColor = p.segmentBG
		r.text.Color = p.activeTabFG
		r.text.TextStyle.Bold = true
	} else {
		r.bg.FillColor = colorTransparent
		r.text.Color = p.metaKeyFG
		r.text.TextStyle.Bold = false
	}
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
	r.icon = canvas.NewImageFromResource(icons.Document)
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
