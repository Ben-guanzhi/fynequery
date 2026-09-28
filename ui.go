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
type Client struct {
	app   fyne.App
	win   fyne.Window
	store *db.Store

	mu        sync.Mutex
	currentID string
	selIdx    int // currently selected connection list index, -1 if none

	// query history panel
	historyList *widget.List

	// current target database for the query editor
	currentDB string

	// widgets (kept so callbacks can refresh them)
	connSel    *widget.Select
	connectBtn *widget.Button // toggles 连接/断开 for the selected connection
	schemaRows *fyne.Container
	treeNodes  map[string]*nodeData

	// navigator filter: narrows the loaded objects by label
	treeFilter  string
	filterEntry *keyedEntry

	// app-level keyboard shortcuts, consulted by the canvas and by every entry
	// that can hold focus (see ui_keys.go); keyDocs renders the 快捷键 dialog
	keys    map[keyCombo]keyBinding
	keyDocs []string

	dbSel      *widget.Select
	queryEntry *suggestEntry
	sugBar     *fyne.Container // autocomplete candidates under the editor
	resultGrid *widget.Table

	// grid edit actions, enabled while the result is a writable table
	editCellBtn    *widget.Button
	insertRowBtn   *widget.Button
	deleteRowBtn   *widget.Button
	saveChangesBtn *widget.Button
	discardBtn     *widget.Button
	filterBtn      *widget.Button
	// grid header/rows for display plus the raw result and edit queue that
	// back write-back (changes.go). gridCols holds the plain column names and
	// gridColTypes the declared types the grid header derived from.
	gridHeader   []string
	gridRows     [][]string
	gridCols     []string
	gridColTypes []string
	lastRows     [][]interface{}
	sourceRef    db.TableRef
	edits        *gridEditState
	sortCol      int
	sortAsc      bool
	limitText    string
	dbList       []string
	status       *widget.Label

	// selected cell viewer (full value + copy)
	cellView     *keyedEntry
	selectedCell widget.TableCellID
	hasCell      bool

	// result-set search: term, ordered hits with the spotlight index, and the
	// cell-bar widgets
	gridSearchEntry *searchEntry
	searchHitsLabel *widget.Label
	searchTerm      string
	searchHits      []searchHitKey
	searchHitSet    map[searchHitKey]bool
	searchCur       int

	// schema navigator state: node cache keyed by uid, which branches are
	// open, and the highlighted table node
	expanded     map[string]bool
	selectedNode string

	// rendered navigator rows memoized by uid + every input the row widget
	// reads; a rebuild whose rows are unchanged reuses the widgets and can
	// skip the re-layout entirely
	rowCache map[rowCacheKey]fyne.CanvasObject

	// per-connection+database table-name memo for SQL completion, warmed by a
	// background load; sugLoading marks in-flight loads
	sugTables  map[string][]string
	sugLoading map[string]bool

	// per-table foreign-key memo for the cell editor's value picker
	fkCache map[string][]db.ForeignKeyInfo

	// quick-open box results
	searchResults []db.ObjectMatch

	// query tabs: independent editors sharing one result grid; c.queryEntry
	// always aliases the active tab's editor
	qTabs  *container.AppTabs
	qItems []*queryTab
	tabSeq int

	// history panel: filter text and the filtered newest-first view the list
	// renders (rebuilt on every change)
	histFilter      string
	historyView     []db.HistoryEntry
	histFilterEntry *keyedEntry

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
	// cancelFn aborts the in-flight query; guarded by c.mu.
	cancelFn context.CancelFunc
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
		app:        a,
		win:        w,
		store:      db.NewStore(),
		treeNodes:  make(map[string]*nodeData),
		expanded:   make(map[string]bool),
		rowCache:   make(map[rowCacheKey]fyne.CanvasObject),
		sugTables:  make(map[string][]string),
		sugLoading: make(map[string]bool),
		fkCache:    make(map[string][]db.ForeignKeyInfo),
		edits:      newGridEditState(),
		sortCol:    -1,
	}
	// The layout + editor drafts are read once here; the only writer is
	// saveWindowSettings on close, so no locking is needed.
	c.uiSet = loadUISettings(uiSettingsPath())
	c.asyncRun = true
	c.dispatch = fyne.Do
	c.keys = make(map[keyCombo]keyBinding)
	return c
}

// closeAll closes every live database pool before quitting.
func (c *Client) closeAll() {
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
	if t := c.uiSet.ActiveTab; t > 0 && t < len(c.qItems) {
		c.qTabs.SelectTabIndex(t)
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
	if c.qTabs != nil {
		s.ActiveTab = c.qTabs.CurrentTabIndex()
	}
	s.Tabs = s.Tabs[:0]
	for _, t := range c.qItems {
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
	if c.queryEntry != nil && c.win != nil {
		c.win.Canvas().Focus(c.queryEntry)
	}
}

// setStatus updates the bottom status bar.
func (c *Client) setStatus(text string) {
	if c.status != nil {
		c.status.SetText(text)
	}
}
