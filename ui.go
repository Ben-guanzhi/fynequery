package main

import (
	"context"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// Client holds application state and the widgets that must be refreshed live.
type Client struct {
	app fyne.App
	win fyne.Window

	mu        sync.Mutex
	saved     []ConnConfig
	live      map[string]*Conn
	currentID string
	connFile  string
	idSeq     int
	selIdx    int // currently selected connection list index, -1 if none

	// query history
	history     []HistoryEntry
	historySeq  int64
	historyFile string
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
	filterEntry *widget.Entry

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
	sourceRef    TableRef
	edits        *gridEditState
	sortCol      int
	sortAsc      bool
	limitText    string
	dbList       []string
	status       *widget.Label

	// selected cell viewer (full value + copy)
	cellView     *widget.Entry
	selectedCell widget.TableCellID
	hasCell      bool

	// schema navigator state: node cache keyed by uid, which branches are
	// open, and the highlighted table node
	expanded     map[string]bool
	selectedNode string

	// per-connection+database table-name memo for SQL completion, warmed by a
	// background load; sugLoading marks in-flight loads
	sugTables  map[string][]string
	sugLoading map[string]bool

	// per-table foreign-key memo for the cell editor's value picker
	fkCache map[string][]ForeignKeyInfo

	// quick-open box results
	searchResults []ObjectMatch

	// query tabs: independent editors sharing one result grid; c.queryEntry
	// always aliases the active tab's editor
	qTabs  *container.AppTabs
	qItems []*queryTab
	tabSeq int

	// history panel: filter text and the filtered newest-first view the list
	// renders (rebuilt on every change)
	histFilter      string
	historyView     []HistoryEntry
	histFilterEntry *widget.Entry

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
		live:       make(map[string]*Conn),
		treeNodes:  make(map[string]*nodeData),
		expanded:   make(map[string]bool),
		sugTables:  make(map[string][]string),
		sugLoading: make(map[string]bool),
		fkCache:    make(map[string][]ForeignKeyInfo),
		edits:      newGridEditState(),
		sortCol:    -1,
	}
	c.connFile = connFilePath()
	c.loadConnections()
	c.historyFile = historyFilePath()
	c.loadHistory()
	// Seed the ID sequence past any existing "c<n>" ids; see nextConnSeq.
	c.idSeq = nextConnSeq(c.saved)
	c.asyncRun = true
	c.dispatch = fyne.Do
	return c
}

// closeAll closes every live database pool before quitting.
func (c *Client) closeAll() {
	c.mu.Lock()
	ids := make([]string, 0, len(c.live))
	for id := range c.live {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.disconnect(id)
	}
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

	// Ctrl+P / Cmd+P opens the quick-open box from anywhere.
	ctrlP := &desktop.CustomShortcut{KeyName: fyne.KeyP, Modifier: fyne.KeyModifierControl}
	cmdP := &desktop.CustomShortcut{KeyName: fyne.KeyP, Modifier: fyne.KeyModifierSuper}
	c.win.Canvas().AddShortcut(ctrlP, func(fyne.Shortcut) { c.showSearchDialog() })
	c.win.Canvas().AddShortcut(cmdP, func(fyne.Shortcut) { c.showSearchDialog() })
	copySel := &desktop.CustomShortcut{KeyName: fyne.KeyC, Modifier: fyne.KeyModifierControl}
	c.win.Canvas().AddShortcut(copySel, func(fyne.Shortcut) { c.copyCell() })
	ctrlN := &desktop.CustomShortcut{KeyName: fyne.KeyN, Modifier: fyne.KeyModifierControl}
	cmdN := &desktop.CustomShortcut{KeyName: fyne.KeyN, Modifier: fyne.KeyModifierSuper}
	c.win.Canvas().AddShortcut(ctrlN, func(fyne.Shortcut) { c.addQueryTab() })
	c.win.Canvas().AddShortcut(cmdN, func(fyne.Shortcut) { c.addQueryTab() })

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

// saveWindowSettings captures the window geometry, split positions and the
// active query tab into the persisted ui settings.
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
