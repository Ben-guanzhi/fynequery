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
	// lastRows keeps the unformatted result the edit queue diffs against -
	// and it is what tells a real SQL NULL from a text column that happens
	// to hold the string "NULL".
	header     []string
	rows       [][]string
	cols       []string
	colTypes   []string
	lastRows   [][]interface{}
	alignRight []bool // per-column right alignment, cached when the header is set
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
	// reads; objects adds the engine's non-table names (Oracle: routines,
	// sequences, synonyms, database links); loading marks the in-flight fetch.
	tables         map[string][]string
	objects        map[string][]string
	loading        map[string]bool
	loadingObjects map[string]bool
	// fks memoizes each table's foreign keys for the cell editor's value
	// picker, which opens on every edit while the catalog rarely changes.
	fks map[string][]db.ForeignKeyInfo
}

// pickerState is the sidebar's connection picker: the selector, the 连接/断开
// toggle beside it, the 测试 button, and the list index it currently stands
// for (-1 for none).
type pickerState struct {
	sel    *widget.Select
	toggle *widget.Button
	test   *widget.Button
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
	// selected is the list row the 删除选中 button acts on; -1 for none.
	selected int
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
	// busy says one tracked run is in flight. It exists because a page turn is a
	// re-query: without the guard it started a second run that overwrote cancelFn,
	// so 取消 stopped the newest run and let the old one land on top of it, and the
	// grid ended up showing page A under a bar that said page B.
	busy bool
	// runGen is the run generation. Streaming rendering stamps the generation it
	// started with and drops its batches once a newer run has begun, so a slow
	// big result cannot land on top of the query that replaced it.
	runGen int
	cache  memoCache

	// The rest is UI-thread state, grouped by what moves together: the sidebar
	// pickers, the schema navigator, the history panel and the editor tabs.
	picker pickerState
	dbs    dbPicker
	tree   treeState
	hist   histState
	tabs   tabStack

	// testCancel and connectCancel hold the abandon handles of in-flight
	// handshakes (Client.background's return value). Both are UI-thread
	// owned: 停止 clicks read them, and only the handshake's own callback
	// or a stop click ever clears them.
	testCancel    func()
	connectCancel func()
	// exportCancel abandons a running INSERT export (its catalog read) when a
	// new export starts, so a slow first export cannot linger behind a second.
	// exportFile is the handle the in-flight INSERT export still holds open;
	// abandoning it must close that too, since the dropped callback would
	// otherwise leak the writer.
	exportCancel func()
	exportFile   fyne.URIWriteCloser

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
	// crumb is the breadcrumb row over the grid (see ui_crumb.go): the < >
	// page steppers reusing the server-side pager, and the centred table name.
	crumb crumbState
	// details is the right-hand TablePlus details pane (see ui_details.go):
	// the selected row's field values, or the table's catalog counts.
	details detailsState

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
	lower            string // 预计算的小写标签，避免过滤时重复 ToLower（CLRS ch3 预处理）
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
		cache: memoCache{tables: make(map[string][]string), objects: make(map[string][]string), loading: make(map[string]bool), loadingObjects: make(map[string]bool), fks: make(map[string][]db.ForeignKeyInfo)},
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

// closeAll flushes what the throttled writes left pending, abandons any
// in-flight export, and closes every live database pool before quitting.
func (c *Client) closeAll() {
	c.releaseExport()
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
	c.bindKey(fyne.KeyW, "关闭当前查询标签", func() { c.closeActiveTab() })
	// Ctrl+1…9 jumps between tabs; one doc line covers the whole run so the
	// 快捷键 dialog stays short instead of listing nine near-identical rows.
	for i, key := range []fyne.KeyName{fyne.Key1, fyne.Key2, fyne.Key3, fyne.Key4, fyne.Key5,
		fyne.Key6, fyne.Key7, fyne.Key8, fyne.Key9} {
		idx := i
		c.bindKey(key, "", func() { c.selectTab(idx) })
	}
	c.docKey("Ctrl / Cmd + 1 … 9    切到第 1-9 个查询标签")
	c.bindKey(fyne.KeyReturn, "执行光标所在的那条语句（有选区则执行选区）", func() { c.runCurrent() })
	c.bindKey(fyne.KeyEnter, "", func() { c.runCurrent() }) // same action, numpad Enter
	// Ctrl+Shift+Enter is what Ctrl+Enter used to do unconditionally: run the
	// tab from top to bottom. It stays reachable because a script that has to
	// run as a whole still has to run as a whole.
	c.bindKeyMod(fyne.KeyReturn, []fyne.KeyModifier{
		fyne.KeyModifierControl | fyne.KeyModifierShift,
		fyne.KeyModifierSuper | fyne.KeyModifierShift,
	}, "执行编辑器中的全部语句", c.runAllStatements)
	c.bindKeyMod(fyne.KeyEnter, []fyne.KeyModifier{
		fyne.KeyModifierControl | fyne.KeyModifierShift,
		fyne.KeyModifierSuper | fyne.KeyModifierShift,
	}, "", c.runAllStatements)
	c.bindKey(fyne.KeyO, "打开 .sql 脚本到当前标签", func() { c.showOpenScriptDialog() })
	c.bindKey(fyne.KeyS, "把当前标签保存为 .sql 脚本", func() { c.showSaveScriptDialog() })
	c.bindKey(fyne.KeyF, "聚焦「在结果中搜索」", c.focusResultSearch)
	c.bindKeyMod(fyne.KeyF, []fyne.KeyModifier{
		fyne.KeyModifierControl | fyne.KeyModifierShift,
		fyne.KeyModifierSuper | fyne.KeyModifierShift,
	}, "格式化当前标签的 SQL", c.formatCurrentSQL)
	c.docKey("Tab    在编辑器内插入缩进（两个空格，不跳出编辑器）")
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
	if c.uiSet.LastDatabase != "" {
		// The 数据库 dropdown restores itself from the live catalog once a
		// connection is open; remembering the choice keeps the picker's own
		// target in step between starts.
		c.currentDB = c.uiSet.LastDatabase
	}
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

// background runs one server round trip off the event thread and hands back
// the way to give up on it. Every catalog reader takes a context now, so each
// caller needs a thread to run it on and a way to stop: on a remote server a
// catalog read can take longer than the user is willing to wait, and one that
// runs on the Fyne thread freezes the window while it is out - the 取消 button
// cannot help because the click never gets delivered.
//
// work runs with the context it is handed and must use it; after runs on the
// event thread with whatever work captured. The returned cancel is idempotent
// and belongs to the thing that owns the round trip - a dialog passes it to
// OnClosed, so a dismissed dialog does not leave a query running on the server.
// A round trip the cancel interrupted reports nothing: its answer belongs to a
// fragment, a filter or a dialog that is gone, and letting it land is how the
// screen ends up showing an answer nobody asked for any more. A round trip that
// had already finished still reports, because the next one is queued behind it
// and cannot be overtaken by it.
//
// Like loadChildren, it stays inline when asyncRun is off: a test
// gets a deterministic call instead of a goroutine it would have to wait for.
func (c *Client) background(work func(context.Context), after func()) (cancel func()) {
	ctx, stop := context.WithCancel(context.Background())
	var once sync.Once
	giveUp := func() { once.Do(stop) }
	if !c.asyncRun || c.dispatch == nil {
		work(ctx)
		stop()
		if after != nil {
			after()
		}
		return giveUp
	}
	go func() {
		work(ctx)
		interrupted := ctx.Err() != nil
		c.dispatch(func() {
			stop()
			if !interrupted && after != nil {
				after()
			}
		})
	}()
	return giveUp
}

// focusEditor returns the keyboard to the SQL editor. Typing into a stale
// button is what made the editor look dead after clicking 连接.
func (c *Client) focusEditor() {
	if c.tabs.entry != nil && c.win != nil {
		c.win.Canvas().Focus(c.tabs.entry)
	}
}

// focusResultSearch hands the keyboard to the 在结果中搜索 box (Ctrl+F): it
// sits in the cell bar under the grid and is otherwise only found by eye.
func (c *Client) focusResultSearch() {
	if c.find.entry != nil && c.win != nil {
		c.win.Canvas().Focus(c.find.entry)
	}
}

// setStatus updates the bottom status bar with an informational line. Every
// call resets the importance, so a red error line cannot linger while an
// informational message follows it.
func (c *Client) setStatus(text string) {
	if c.status == nil {
		return
	}
	c.status.Importance = widget.MediumImportance
	c.status.SetText(text)
}

// setStatusError writes the same line in danger red: failures (连接/测试/
// 导出/脚本读写) are the messages the eye should find first in the strip.
func (c *Client) setStatusError(text string) {
	if c.status == nil {
		return
	}
	c.status.Importance = widget.DangerImportance
	c.status.SetText(text)
}

// setStatusWarn writes an attention line (warnings, blocked actions) in the
// theme's warning colour - more urgent than info, less than a failure.
func (c *Client) setStatusWarn(text string) {
	if c.status == nil {
		return
	}
	c.status.Importance = widget.WarningImportance
	c.status.SetText(text)
}

// appendStatus extends the line just written, for the verdict a plain "返回 N 行"
// cannot convey: the row cap ran, declined, and the grid is showing more than
// the number the toolbar says.
func (c *Client) appendStatus(text string) {
	if c.status != nil && text != "" {
		c.status.SetText(c.status.Text + text)
	}
}
