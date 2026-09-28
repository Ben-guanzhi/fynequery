package main

import (
	"context"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// Client holds application state and the widgets that must be refreshed live.
// All connection, catalog and persistence state lives behind the db.Store
// barrier; Client owns only what the UI thread shows or edits.
// pagerState is the paged-result window and the widgets that drive it (see
// ui_paging.go). Grouping them keeps the paging machine's state in one place:
// a page is always the triple stmt/no/size, and the note belongs to that
// window rather than to the client as a whole.
type pagerState struct {
	// on is the toolbar switch; limitSel/limitText are the row-cap selector it
	// supersedes.
	on        bool
	limitSel  *widget.Select
	limitText string
	check     *widget.Check
	// bar, label and sizeSel make up the bar over the grid.
	bar     *fyne.Container
	label   *widget.Label
	sizeSel *widget.Select
	// stmt is the statement the on-screen page came from, empty while the
	// result is not pageable; no is its 1-based window and size the number of
	// rows one window holds.
	stmt    string
	no      int
	size    int
	prevBtn *widget.Button
	nextBtn *widget.Button
	// note is the bar's verdict on the window's row order: either the sort the
	// pager injected ("已按主键 id 排序") or the warning that nothing makes
	// consecutive pages disjoint ("⚠ 排序不稳定").
	note string
}

// findState is the search inside the current result: the term, the ordered hits
// with the spotlight index, and the widgets in the cell bar.
type findState struct {
	entry  *searchEntry
	label  *widget.Label
	term   string
	hits   []searchHitKey
	hitSet map[searchHitKey]bool
	cur    int
}

// gridState is the result on screen: the table widget, the header and rows it
// displays, the raw result and edit queue behind write-back, and the current
// sort and selection.
type gridState struct {
	table *widget.Table
	// header and rows are what the grid shows; cols holds the plain column
	// names and colTypes the declared types the header was derived from.
	// lastRows keeps the unformatted result the edit queue diffs against.
	header   []string
	rows     [][]string
	cols     []string
	colTypes []string
	lastRows [][]interface{}
	// src names the single table a writable result came from; edits queues the
	// changes to write back (see changes.go).
	src   db.TableRef
	edits *gridEditState
	// sortCol is the display-only sort column, -1 for none.
	sortCol int
	sortAsc bool
	// view shows the selected cell's full value; cell and hasCell are the
	// selection itself.
	view    *keyedEntry
	cell    widget.TableCellID
	hasCell bool
}

// rowActions are the bottom-bar buttons that act on the on-screen result.
type rowActions struct {
	filter      *widget.Button
	editCell    *widget.Button
	insertRow   *widget.Button
	deleteRow   *widget.Button
	saveChanges *widget.Button
	discard     *widget.Button
}

// memoCache groups the per-session introspection memos. Background loads fill
// them off the UI thread, so every access holds c.mu.
type memoCache struct {
	// tables is the per-connection+database table-name list SQL completion
	// reads; loading marks the keys whose fetch is in flight.
	tables  map[string][]string
	loading map[string]bool
	// fks memoizes each table's foreign keys for the cell editor's value
	// picker, which opens on every edit while the catalog rarely changes.
	fks map[string][]db.ForeignKeyInfo
}

// pickerState is the sidebar's connection picker: the selector, the 连接/断开
// toggle beside it, and the list index it currently stands for (-1 for none).
type pickerState struct {
	sel    *widget.Select
	toggle *widget.Button
	idx    int
}

// dbPicker is the query toolbar's database selector and its option list.
type dbPicker struct {
	sel  *widget.Select
	list []string
}

// treeState is the schema navigator: the node cache keyed by uid, which
// branches are open, the highlighted table node, and the filter that narrows
// the loaded objects by label.
type treeState struct {
	nodes    map[string]*nodeData
	expanded map[string]bool
	// selected is the highlighted table node; term is the filter text.
	selected    string
	term        string
	rows        *fyne.Container
	filterEntry *keyedEntry
	// cache memoizes rendered navigator rows by uid + every input the row
	// widget reads; a rebuild whose rows are unchanged reuses the widgets and
	// can skip the re-layout entirely.
	cache map[rowCacheKey]fyne.CanvasObject
}

// histState is the query history panel: the list, its filter text, and the
// filtered newest-first view the list renders (rebuilt on every change).
type histState struct {
	list        *widget.List
	filter      string
	view        []db.HistoryEntry
	filterEntry *keyedEntry
}

// tabStack is the query editor stack: independent editors sharing one result
// grid. entry always aliases the active tab's editor, and sug is the
// autocomplete candidate bar under it.
type tabStack struct {
	bar   *container.AppTabs
	items []*queryTab
	seq   int
	entry *suggestEntry
	sug   *fyne.Container
}

type Client struct {
	app   fyne.App
	win   fyne.Window
	store *db.Store

	// mu guards currentID, currentDB, cancelFn and the memos below: the query
	// worker reads and writes part of this state off the UI thread.
	mu        sync.Mutex
	currentID string
	// currentDB is the query editor's target database.
	currentDB string
	// cancelFn aborts the in-flight query.
	cancelFn context.CancelFunc
	cache    memoCache

	// The rest is UI-thread state, grouped by what moves together: the sidebar
	// pickers, the schema navigator, the history panel and the editor tabs.
	picker pickerState
	dbs    dbPicker
	tree   treeState
	hist   histState
	tabs   tabStack

	// app-level keyboard shortcuts, consulted by the canvas and by every entry
	// that can hold focus (see ui_keys.go); keyDocs renders the 快捷键 dialog
	keys    map[keyCombo]keyBinding
	keyDocs []string

	// result paging (see ui_paging.go) and the in-result search (see ui_search.go)
	page pagerState
	find findState

	// the on-screen result: the table widget, the rows it displays, the current
	// sort and selection, and the edit queue that feeds write-back (changes.go)
	grid gridState
	// bottom-bar actions, enabled while the result is a writable table
	act rowActions

	status *widget.Label

	// quick-open box results
	searchResults []db.ObjectMatch

	// persisted ui state (theme, window geometry, splits, active tab)
	uiSet  uiSettings
	hSplit *container.Split
	vSplit *container.Split

	// dispatch marshals work back to the UI thread: fyne.Do in production,
	// an inline run in tests.
	dispatch func(f func())
	// asyncRun runs queries on a background goroutine (true in the live app,
	// false in tests for determinism).
	asyncRun bool
	// run/cancel buttons, toggled by setRunning.
	runBtn       *widget.Button
	cancelRunBtn *widget.Button
}

const treeRootUID = "-"

// nodeData backs one node in the schema tree.
type nodeData struct {
	kind             string // "root" | "db" | "schema" | "table"
	label            string
	db, schema, name string
	uid              string
	loaded           bool
	loading          bool     // a background fetch is in flight for this node
	children         []string // child uids
	typ              string   // "table" | "view" for table nodes
	parent           string   // set on error nodes so tapping them retries the parent
}

// queryTab is one editor tab: its strip item, its editor with a line-number
// gutter (both scroll together in the tab's own scroller), and bookkeeping.
// dirty marks text that changed since the tab last ran.
type queryTab struct {
	item      *container.TabItem
	entry     *suggestEntry
	gutter    *widget.Entry // disabled line-number entry, same metrics as the editor
	scroller  *container.Scroll
	baseTitle string
	dirty     bool
}

func newClient(a fyne.App, w fyne.Window) *Client {
	c := &Client{
		app:   a,
		win:   w,
		store: db.NewStore(),
		tree:  treeState{nodes: make(map[string]*nodeData), expanded: make(map[string]bool), cache: make(map[rowCacheKey]fyne.CanvasObject)},
		cache: memoCache{tables: make(map[string][]string), loading: make(map[string]bool), fks: make(map[string][]db.ForeignKeyInfo)},
		grid:  gridState{edits: newGridEditState(), sortCol: -1},
	}
	// The layout + editor drafts are read once here; the only writer is
	// saveWindowSettings on close, so no locking is needed.
	c.uiSet = loadUISettings(uiSettingsPath())
	if validPageSize(c.uiSet.PageSize) {
		c.page.size = c.uiSet.PageSize
	}
	c.asyncRun = true
	c.dispatch = fyne.Do
	c.keys = make(map[keyCombo]keyBinding)
	return c
}

// closeAll flushes what the throttled writes left pending and closes every live
// database pool before quitting.
func (c *Client) closeAll() {
	c.store.PersistHistory()
	c.store.CloseAll()
	c.mu.Lock()
	c.currentID = ""
	c.mu.Unlock()
}

// buildUI assembles the TablePro-style layout: one sidebar with the
// connection panel on top, the schema navigator in the middle and the history
// panel at the bottom; the editor and grid take the rest of the window.
func (c *Client) buildUI() fyne.CanvasObject {
	sidebar := container.NewBorder(
		c.buildConnectionsPanel(), // top: connection picker + actions
		c.buildHistoryPanel(),     // bottom: recent statements
		nil, nil,
		c.buildSchemaView(), // center: the navigator
	)

	// Ctrl+P / Cmd+P opens the quick-open box, Ctrl+N a fresh tab, Ctrl+Enter
	// runs, Ctrl+O / Ctrl+S read and write the active tab as a .sql script.
	// Every one of them goes through the shortcut table so it also fires while
	// a text entry holds focus (see ui_keys.go). The main Enter is
	// fyne.KeyReturn and the numpad Enter is fyne.KeyEnter, so both bind.
	c.bindKey(fyne.KeyP, "快速打开：按表名片段搜表/视图", func() { c.showSearchDialog() })
	c.bindKey(fyne.KeyN, "新建查询标签", func() { c.addQueryTab() })
	c.bindKey(fyne.KeyReturn, "执行编辑器中的 SQL", func() { c.runCurrent() })
	c.bindKey(fyne.KeyEnter, "", func() { c.runCurrent() }) // same action, numpad Enter
	c.bindKey(fyne.KeyO, "打开 .sql 脚本到当前标签", func() { c.showOpenScriptDialog() })
	c.bindKey(fyne.KeyS, "把当前标签保存为 .sql 脚本", func() { c.showSaveScriptDialog() })
	// Copy stays a canvas-level registration on purpose: an entry that claimed
	// Ctrl+C from the table could no longer copy its own selection.
	copySel := &desktop.CustomShortcut{KeyName: fyne.KeyC, Modifier: fyne.KeyModifierControl}
	c.win.Canvas().AddShortcut(copySel, func(fyne.Shortcut) { c.copyCell() })
	c.docKey("Ctrl+C    复制选中单元格（焦点不在输入框时）")
	c.docKey("Enter / Shift+Enter    在结果搜索框内跳下一个 / 上一个命中")

	query := c.buildQueryView()
	split := container.NewHSplit(sidebar, query)
	split.Offset = 0.22
	c.refreshConnections()
	c.refreshHistory()
	c.refreshSchema() // shows the 未连接 root until a connection is opened

	c.hSplit = split
	if r := c.uiSet.SidebarRatio; r > 0.05 && r < 0.95 {
		split.Offset = float64(r)
	}
	if t := c.uiSet.ActiveTab; t > 0 && t < len(c.tabs.items) {
		c.tabs.bar.SelectTabIndex(t)
	}
	return split
}

// saveWindowSettings captures the window geometry, split positions, the
// active query tab and every tab's editor draft into the persisted ui
// settings.
func (c *Client) saveWindowSettings(w fyne.Window) {
	s := c.uiSet
	// The canvas size is what layouts see; on desktop Window.Resize requests
	// exactly this content size back, so the restore does not drift.
	cv := w.Canvas().Size()
	s.WindowWidth, s.WindowHeight = cv.Width, cv.Height
	if c.hSplit != nil {
		s.SidebarRatio = float32(c.hSplit.Offset)
	}
	if c.vSplit != nil {
		s.EditorRatio = float32(c.vSplit.Offset)
	}
	if c.tabs.bar != nil {
		s.ActiveTab = c.tabs.bar.CurrentTabIndex()
	}
	s.Tabs = s.Tabs[:0]
	for _, t := range c.tabs.items {
		s.Tabs = append(s.Tabs, queryTabDraft{Content: t.entry.Text, Dirty: t.dirty})
	}
	c.uiSet = s
	saveUISettings(uiSettingsPath(), s)
}

// runAsync runs work off the UI thread and marshals the follow-up back to the
// main goroutine (fyne.Do in production, inline in tests) with work's result.
// The window stays responsive while network/catalog work happens.
func (c *Client) runAsync(work func(), after func()) {
	if c.dispatch == nil {
		work()
		if after != nil {
			after()
		}
		return
	}
	go func() {
		work()
		c.dispatch(func() {
			if after != nil {
				after()
			}
		})
	}()
}

// focusEditor returns the keyboard to the SQL editor. Typing into a stale
// button is what made the editor look dead after clicking 连接.
func (c *Client) focusEditor() {
	if c.tabs.entry != nil && c.win != nil {
		c.win.Canvas().Focus(c.tabs.entry)
	}
}

// setStatus updates the bottom status bar.
func (c *Client) setStatus(text string) {
	if c.status != nil {
		c.status.SetText(text)
	}
}
