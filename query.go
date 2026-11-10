package main

// Query tab: SQL editor with beautify/run buttons and a results grid.
// Structure pane: column list for the Structure segment.

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
	"fynequery/icons"
	"fynequery/internal/sqlscan"
	"fynequery/proto"
)

// colorWhite is used for text on accent backgrounds.
var colorWhite = color.RGBA{R: 255, G: 255, B: 255, A: 255}

// queryLimit caps interactive query results.
const queryLimit = 5000

// Editor/result split offsets for the query pane: editor-dominant while there
// is no result (the bottom just shows a hint), results-favoured once one loads.
const (
	splitNoResult   = 0.78
	splitWithResult = 0.45
)

// queryPane is the SQL tab content.
type queryPane struct {
	widget.BaseWidget
	editor       *widget.Entry
	resultSlot   *fyne.Container   // holds the current result grid (replaced per run)
	searchWrap   fyne.CanvasObject // result search box (hidden until a result loads)
	noResult     fyne.CanvasObject // centered run-a-query hint (shown until a result loads)
	split        *container.Split  // editor/result splitter
	resultActive bool              // whether a result is shown (drives the split offset)
	status       *statusBar
	timer        *canvas.Text
	posLabel     *widget.Label // "第 1 行, 第 1 列, 位置 0"
	limitSel     *widget.Select
	runBtn       *accentButton
	window       fyne.Window
	conn         Connection
	sess         db.Session
	side         *sidebar
	running      bool

	// sessFn returns the workspace's current session. The workspace re-dials
	// (and closes the old session) when the user picks a database, so the pane
	// must not keep a stale session captured at build time.
	sessFn        func() db.Session
	onReconnect   func(func(db.Session))         // workspace dials a fresh session, then calls back (timeout dialog)
	onRowSelect   func(data *TableData, row int) // a result row was selected
	onLog         func(string)                   // workspace console sink (optional)
	colCache      map[string][]string            // table -> column names (completion)
	colVersion    int                            // bumped when colCache gains entries (invalidates sugCache)
	colInflight   map[string]*colCall            // in-flight Columns() fetches, deduplicated per table
	sug           *sugCache                      // built + sorted completion candidates (guarded by colMu)
	colMu         sync.Mutex                     // guards colCache / colVersion / colInflight / sug
	shortcutBound bool

	// runGen discards result batches from a superseded run (TablePlus
	// generation guard). bumpRun returns the new generation. Atomic: bumpRun
	// runs on the query's background goroutine while the UI thread reads it
	// inside every batch callback — two concurrent runs may bump at once.
	runGen atomic.Int32
	// continueOnError keeps a multi-statement run going past failures
	// (TablePlus "Return on Error" inverted; we default to stopping).
	continueOnError bool

	// pinned results (TablePlus "Pin result"): snapshots that survive re-runs,
	// shown as a chip row above the live grid.
	pins      []pinnedResult
	pinBar    *fyne.Container
	activePin int // index of the pin currently shown in resultSlot (-1 = live)

	// run-result tabs: every statement of the last Run keeps its grid alive and
	// switchable (Run All used to keep only the last statement's result). The
	// switcher bar shows from the second tab on; a fresh Run resets the set,
	// while a retry of statement i replaces that tab in place.
	resultTabs []*queryResultTab
	resultSel  int // tab currently shown in resultSlot (-1 = none)
	resultStmt int // statement index of the newest tab (-1 = none yet)
	resultBar  *fyne.Container
	// find box + counter for in-result search on this pane's result grid.
	findEntry   *gridSearchEntry
	findCounter *widget.Label

	// as-you-type completion popup (Ctrl+Space still opens the classic menu).
	autoPop      *widget.PopUp
	autoProxy    *editorFocusProxy // forwards keys to the editor while popup is open
	autoList     *fyne.Container
	autoRows     []*suggestionRow // rows in autoList, for keyboard selection
	autoWords    []string
	autoSel      int  // highlighted suggestion (-1 = none)
	autoStart    int  // rune offset where the replaced word starts
	autoWordLen  int  // rune length of the word being replaced
	autoGen      int  // debounce/race guard
	autoSuppress bool // set while inserting a completion (no re-trigger)
	// autoTimer is the pending debounce timer. FQ-10: keeping the handle lets
	// scheduleAutoComplete cancel the previous tick instead of stacking timers.
	autoTimer *time.Timer
}

func newQueryPane(w fyne.Window, c Connection, sess db.Session, side *sidebar) *queryPane {
	qp := &queryPane{window: w, conn: c, sess: sess, side: side, resultSel: -1, resultStmt: -1}
	qp.ExtendBaseWidget(qp)
	return qp
}

// session returns the current live session. The workspace re-dials and closes
// the previous session when the user switches databases; preferring sessFn
// avoids using that stale connection (which the plugin reports as "closed").
func (qp *queryPane) session() db.Session {
	if qp.sessFn != nil {
		return qp.sessFn()
	}
	return qp.sess
}

// logQuery forwards one console line to the workspace (no-op without a hook).
func (qp *queryPane) logQuery(format string, args ...interface{}) {
	if qp.onLog != nil {
		qp.onLog(fmt.Sprintf(format, args...))
	}
}

// oneLine collapses a SQL statement to its first line and truncates it so the
// console stays readable for long scripts.
func oneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// queryPlaceholder returns the editor placeholder for a connection's driver.
// A KV session runs one command per line (replies render as a single "reply"
// column), so the example matches what actually executes there instead of
// teaching SQL that the driver would reject.
func queryPlaceholder(driver string) string {
	if driver == "redis" {
		return "逐行输入 Redis 命令（每行一条，分号可省略），例如：\nHGETALL myhash\nSCAN 0 MATCH user:* COUNT 100\nSET greeting \"hello\"\n\nCtrl+Enter 执行 · 回复按行拼成 reply 表"
	}
	return "Write SQL here, e.g.\nSELECT id, name FROM users LIMIT 20;\n\nCtrl+Enter 执行当前语句 · Ctrl+Space 补全关键字 / 表 / 列"
}

func (qp *queryPane) CreateRenderer() fyne.WidgetRenderer {
	qp.editor = widget.NewMultiLineEntry()
	qp.editor.TextStyle.Monospace = true  // SQL reads best in a monospace font
	qp.editor.Wrapping = fyne.TextWrapOff // keep statements on one line; scroll horizontally
	gutter := newLineGutter(qp.editor)    // line-number gutter, scroll-synced to the editor
	qp.editor.SetPlaceHolder(queryPlaceholder(qp.conn.Driver))
	// keep the cursor-position readout roughly current and offer completions
	// while typing (the popup updates as the word under the cursor changes)
	qp.editor.OnChanged = func(string) {
		qp.refreshPos()
		qp.scheduleAutoComplete()
		gutter.sync()
	}

	qp.posLabel = widget.NewLabel("Line 1, Col 1, Pos 0")
	qp.limitSel = widget.NewSelect([]string{"100 rows", "500 rows", "1000 rows", "5000 rows (max)"}, func(string) {})
	qp.limitSel.SetSelected("100 rows")

	qp.timer = canvas.NewText("", pal().statusFG)
	qp.timer.TextSize = 12

	runBtn := newAccentButton("Run Current")
	runBtn.onTap = func() { qp.run() }
	qp.runBtn = runBtn
	beautify := newFlatButton("Beautify")
	beautify.onTap = func() { qp.beautify() }
	completeBtn := newFlatButton("Complete")
	completeBtn.onTap = func() { qp.complete() }
	runAll := newFlatButton("Run All")
	runAll.onTap = func() { qp.runAll() }
	// Visible entry point for the in-result search bar (also Ctrl+F), so it is
	// discoverable even though the bar starts hidden.
	findBtn := newToolButton(icons.Search, func() { qp.toggleFindBar() })
	contErr := widget.NewCheck("Continue on error", func(on bool) { qp.continueOnError = on })

	// toolbar sits below the editor (TablePlus SQL editor): position | limit |
	// Beautify | Complete | Run All | Run Current (doubles as Cancel) | timer
	toolbar := container.NewBorder(nil, nil,
		qp.posLabel,
		container.NewHBox(qp.limitSel, spacerW(10), beautify, spacerW(6), completeBtn, spacerW(6),
			runAll, spacerW(8), findBtn, spacerW(8), contErr, spacerW(6), runBtn, spacerW(10), qp.timer),
		layout.NewSpacer(),
	)
	editorArea := container.NewBorder(nil, nil, gutter, nil, container.NewPadded(qp.editor))
	editorPane := container.NewBorder(nil, toolbar, nil, nil, editorArea)

	qp.status = newStatusBar()
	initGrid := newDataTable(emptyTableData(), qp.status)
	initGrid.window = qp.window
	qp.resultSlot = container.NewStack(initGrid)

	// in-result search box (TablePlus in-grid spotlight): Enter steps to the
	// next hit, Shift+Enter to the previous. It targets whatever result grid
	// is currently shown, so it keeps working across re-runs.
	qp.findEntry = newGridSearchEntry()
	qp.findEntry.PlaceHolder = "在结果中查找…"
	qp.findEntry.OnChanged = func(s string) {
		if g := qp.resultGrid(); g != nil {
			g.setFindTerm(s)
		}
	}
	qp.findEntry.onNext = func() {
		if g := qp.resultGrid(); g != nil {
			g.stepFind(1)
		}
	}
	qp.findEntry.onPrev = func() {
		if g := qp.resultGrid(); g != nil {
			g.stepFind(-1)
		}
	}
	qp.findCounter = widget.NewLabel("")
	findBox := container.NewHBox(
		widget.NewIcon(icons.Search),
		qp.findEntry,
		qp.findCounter,
	)

	// pinned-result chip row (TablePlus "Pin result"), shown only when there
	// are pins. Clicking a chip shows that snapshot; its × un-pins it.
	qp.pinBar = container.NewHBox()
	qp.pinBar.Hide()

	// run-result tab strip: one chip per statement result of the last Run,
	// shown only from the second result on (see renderResultBar).
	qp.resultBar = container.NewHBox()
	qp.resultBar.Hide()

	// search bar sits just above the result grid
	qp.searchWrap = container.NewBorder(nil, nil, nil, nil, findBox)
	noResult := widget.NewLabelWithStyle("写点 SQL，然后点 Run Current 或按 Ctrl+Enter 执行", fyne.TextAlignCenter, fyne.TextStyle{})
	noResult.Importance = widget.LowImportance
	noResult.Wrapping = fyne.TextWrapWord
	qp.noResult = container.NewCenter(noResult)

	// The result pane stacks the pin/search bars above the grid and the status
	// bar below it. A Border centre stretches the grid to fill the trailing half
	// of the split, so there is no dead space underneath the results.
	resultPane := container.NewBorder(
		container.NewVBox(qp.resultBar, qp.pinBar, qp.searchWrap), qp.status, nil, nil,
		container.NewStack(qp.resultSlot, qp.noResult))

	// Editor (top) and result (bottom) share one draggable split, so the divider
	// adjusts both panes' heights. Making the split the root widget lets it fill
	// the whole tab; wrapping it in a VBox had clamped it to its minimum height
	// and left a large blank area below the results.
	split := container.NewVSplit(editorPane, resultPane)
	qp.split = split
	// No result yet: let the editor dominate; a balanced split is applied when
	// results arrive (see updateResultChrome).
	if g := qp.resultGrid(); g != nil && len(g.data.Cols) > 0 {
		split.SetOffset(splitWithResult)
		qp.resultActive = true
	} else {
		split.SetOffset(splitNoResult)
	}

	qp.bindEditorShortcuts()
	qp.updateResultChrome()
	return widget.NewSimpleRenderer(split)
}

// resultGrid returns the currently displayed result grid, or nil if none.
func (qp *queryPane) resultGrid() *dataTable {
	if qp.resultSlot == nil || len(qp.resultSlot.Objects) == 0 {
		return nil
	}
	if g, ok := qp.resultSlot.Objects[0].(*dataTable); ok {
		return g
	}
	return nil
}

// pinnedResult is one snapshot kept above the live grid.
type pinnedResult struct {
	label string
	data  *TableData
}

// pinResult snapshots the current live result grid into the pin bar
// (TablePlus "Pin result"). Re-running the query keeps the pin viewable.
func (qp *queryPane) pinResult() {
	g := qp.resultGrid()
	if g == nil || g.data == nil || len(g.data.Rows) == 0 {
		showInfo(qp.window, "Pin result", "Run a query first.")
		return
	}
	label := "Pinned " + fmt.Sprint(len(qp.pins)+1)
	if n := resultName(strings.TrimSpace(qp.editor.Text)); n != "" {
		label = n
	}
	qp.pins = append(qp.pins, pinnedResult{label: label, data: cloneTableData(g.data)})
	qp.renderPinBar()
}

// showPin switches the result slot to the pinned snapshot's read-only grid.
func (qp *queryPane) showPin(i int) {
	if i < 0 || i >= len(qp.pins) {
		return
	}
	p := qp.pins[i]
	g := newDataTable(cloneTableData(p.data), qp.status)
	g.window = qp.window
	g.tableName = ""
	g.onRowSelect = func(idx int) {
		if qp.onRowSelect != nil {
			qp.onRowSelect(g.data, idx)
		}
	}
	qp.activePin = i
	qp.resultSlot.Objects = []fyne.CanvasObject{g}
	qp.resultSlot.Refresh()
	qp.bindFind(g)
	qp.updateResultChrome()
	qp.status.setQueryInfo("● " + p.label + " — " + fmt.Sprint(len(p.data.Cols)) + " columns · " +
		fmt.Sprint(len(p.data.Rows)) + " rows (pinned)")
}

// unpin removes a pinned result from the bar.
func (qp *queryPane) unpin(i int) {
	if i < 0 || i >= len(qp.pins) {
		return
	}
	qp.pins = append(qp.pins[:i], qp.pins[i+1:]...)
	if qp.activePin >= len(qp.pins) {
		qp.activePin = -1
	}
	qp.renderPinBar()
}

// renderPinBar rebuilds the chip row from the current pins.
func (qp *queryPane) renderPinBar() {
	if qp.pinBar == nil {
		return
	}
	qp.pinBar.Objects = qp.pinBar.Objects[:0]
	if len(qp.pins) == 0 {
		qp.pinBar.Hide()
		qp.pinBar.Refresh()
		return
	}
	qp.pinBar.Show()
	for i := range qp.pins {
		i := i
		name := qp.pins[i].label
		chip := newFlatButton(name)
		chip.onTap = func() { qp.showPin(i) }
		un := newToolButton(icons.Cancel, func() { qp.unpin(i) })
		un.small = true
		qp.pinBar.Objects = append(qp.pinBar.Objects, chip, un, spacerW(6))
	}
	qp.pinBar.Refresh()
}

// cloneTableData deep-copies a TableData so a pinned snapshot is not mutated
// by later streaming into the live grid.
func cloneTableData(d *TableData) *TableData {
	if d == nil {
		return &TableData{}
	}
	c := &TableData{
		Cols:    append([]ColumnInfo(nil), d.Cols...),
		Fetched: d.Fetched,
		Total:   d.Total,
		Approx:  d.Approx,
		Rows:    make([][]*string, 0, len(d.Rows)),
	}
	for _, r := range d.Rows {
		c.Rows = append(c.Rows, append([]*string(nil), r...))
	}
	return c
}

// ---- run-result tabs --------------------------------------------------------

// queryResultTab is one statement result kept switchable above the grid. Unlike
// a pin it is not a snapshot: the tab holds the very grid the statement streamed
// into, so switching back to it shows the live widget with its find state,
// selection and row count intact.
type queryResultTab struct {
	title  string     // magic "-- name:" label or "stmt N"
	stmt   string     // one-line SQL for debugging/tests
	grid   *dataTable // the grid that landed in resultSlot when the result arrived
	timer  string     // elapsed text restored when this tab is selected
	status string     // result-status line restored when this tab is selected
}

// resetResultTabs drops the previous run's tabs. Only a fresh user-initiated
// Run (runChain) calls this — timeout retries go through runChainAt directly
// and must keep the earlier statements' tabs.
func (qp *queryPane) resetResultTabs() {
	qp.resultTabs = nil
	qp.resultSel = -1
	qp.resultStmt = -1
	qp.renderResultBar()
}

// recordResultTab files a statement's result grid into the tab strip and
// selects it (results land sequentially, so the newest one is the one running),
// returning the tab so the statement's completion callback can store its chrome
// meta on its OWN tab. A result for the same statement index as the newest tab
// supersedes it — that is a retry replacing a partial streaming grid.
func (qp *queryPane) recordResultTab(i int, label, stmt string, g *dataTable) *queryResultTab {
	if g == nil {
		return nil
	}
	title := label
	if title == "" {
		title = fmt.Sprintf("stmt %d", i+1)
	}
	tab := &queryResultTab{title: title, stmt: oneLine(stmt, 80), grid: g}
	if i >= 0 && i == qp.resultStmt && len(qp.resultTabs) > 0 {
		qp.resultTabs[len(qp.resultTabs)-1] = tab
	} else {
		qp.resultTabs = append(qp.resultTabs, tab)
		qp.resultStmt = i
	}
	qp.resultSel = len(qp.resultTabs) - 1
	qp.renderResultBar()
	return tab
}

// setMeta stores the finished statement's timer/status text on its own tab so
// switching back to it restores the chrome. The pointer — NOT the currently
// selected tab — is the write target: the user can switch to another result's
// chip while a slow statement is still running, and writing through resultSel
// would paste this statement's duration onto the one they are looking at.
func (t *queryResultTab) setMeta(timer, status string) {
	if t == nil {
		return
	}
	t.timer = timer
	t.status = status
}

// showResultTab swaps the result slot to tab i's grid and restores its chrome.
func (qp *queryPane) showResultTab(i int) {
	if i < 0 || i >= len(qp.resultTabs) {
		return
	}
	t := qp.resultTabs[i]
	qp.resultSel = i
	qp.activePin = -1 // a run-result tab is a live view, not a pinned one
	qp.resultSlot.Objects = []fyne.CanvasObject{t.grid}
	qp.resultSlot.Refresh()
	qp.bindFind(t.grid)
	qp.updateResultChrome()
	if t.timer != "" {
		qp.timer.Text = t.timer
		qp.timer.Color = pal().statusFG
		qp.timer.Refresh()
	}
	if t.status != "" {
		qp.status.setQueryInfo(t.status)
	}
	qp.renderResultBar()
}

// renderResultBar rebuilds the run-result chip row. It stays hidden while
// fewer than two results are held: a single result needs no switcher (its
// magic name already shows in the status line).
func (qp *queryPane) renderResultBar() {
	if qp.resultBar == nil {
		return
	}
	qp.resultBar.Objects = qp.resultBar.Objects[:0]
	if len(qp.resultTabs) < 2 {
		qp.resultBar.Hide()
		qp.resultBar.Refresh()
		return
	}
	qp.resultBar.Show()
	for j, t := range qp.resultTabs {
		j, t := j, t
		title := t.title
		if j == qp.resultSel {
			title = "▸ " + title
		}
		chip := newFlatButton(title)
		chip.onTap = func() { qp.showResultTab(j) }
		qp.resultBar.Objects = append(qp.resultBar.Objects, chip, spacerW(6))
	}
	qp.resultBar.Refresh()
}

// updateResultChrome shows the "run a query" hint (and hides the result search
// box) until a result with columns is actually loaded.
// toggleFindBar shows or hides the in-result search bar (Ctrl+F). It stays
// hidden until asked for, so the result grid keeps the full height.
func (qp *queryPane) toggleFindBar() {
	if qp.searchWrap == nil {
		return
	}
	if qp.searchWrap.Visible() {
		qp.searchWrap.Hide()
		return
	}
	qp.searchWrap.Show()
	if qp.findEntry != nil {
		if c := fyne.CurrentApp().Driver().CanvasForObject(qp.findEntry); c != nil {
			c.Focus(qp.findEntry)
		}
	}
}

func (qp *queryPane) updateResultChrome() {
	g := qp.resultGrid()
	empty := g == nil || len(g.data.Cols) == 0
	if qp.noResult != nil {
		if empty {
			qp.noResult.Show()
		} else {
			qp.noResult.Hide()
		}
	}
	// The in-result search bar is off by default (Ctrl+F reveals it); only
	// force-hide it when there is no result left to search.
	if qp.searchWrap != nil && empty {
		qp.searchWrap.Hide()
	}
	// Rebalance the editor/result split only on a no-result <-> result
	// transition, so a manual drag is never fought on every refresh.
	active := !empty
	if qp.split != nil && active != qp.resultActive {
		qp.resultActive = active
		if active {
			qp.split.SetOffset(splitWithResult) // give the results room
		} else {
			qp.split.SetOffset(splitNoResult) // editor-dominant, hint only
		}
	}
}

// bindFind wires a result grid's hit counter to this pane's label.
func (qp *queryPane) bindFind(g *dataTable) {
	g.onFindStatus = func(cur, total int) {
		if qp.findCounter == nil {
			return
		}
		if cur == 0 && total == 0 {
			qp.findCounter.SetText("")
		} else {
			qp.findCounter.SetText(fmt.Sprintf("%d/%d", cur, total))
		}
	}
}

// bumpRun advances and returns the run generation, superseding any in-flight
// streaming batches from a previous run.
func (qp *queryPane) bumpRun() int {
	return int(qp.runGen.Add(1))
}

// refreshPos updates the "第 N 行, 第 M 列, 位置 K" readout from the editor text.
// toggleComment toggles a "-- " SQL line comment on the editor's current line
// (Ctrl+/), preserving indentation and keeping the cursor on that line.
func (qp *queryPane) toggleComment() {
	if qp.editor == nil {
		return
	}
	lines := strings.Split(qp.editor.Text, "\n")
	row := qp.editor.CursorRow
	if row < 0 || row >= len(lines) {
		return
	}
	lines[row] = toggleLineComment(lines[row])
	qp.editor.SetText(strings.Join(lines, "\n"))
	qp.editor.CursorRow = row
	qp.editor.CursorColumn = len([]rune(lines[row]))
	qp.editor.Refresh()
	qp.refreshPos()
}

// toggleLineComment adds or removes a leading "-- " on one line, keeping any
// indentation intact.
func toggleLineComment(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(trimmed)]
	switch {
	case strings.HasPrefix(trimmed, "-- "):
		return indent + trimmed[3:]
	case strings.HasPrefix(trimmed, "--"):
		return indent + strings.TrimPrefix(trimmed[2:], " ")
	default:
		return indent + "-- " + trimmed
	}
}

func (qp *queryPane) refreshPos() {
	if qp.posLabel == nil || qp.editor == nil {
		return
	}
	text := qp.editor.Text
	row := strings.Count(text, "\n") + 1
	col := len(text) - (strings.LastIndex(text, "\n") + 1) + 1
	qp.posLabel.SetText(fmt.Sprintf("Line %d, Col %d, Pos %d", row, col, len([]rune(text))))
}

// rowLimit maps the selector choice to a max-rows value for Query.
func (qp *queryPane) rowLimit() int {
	if qp.limitSel == nil {
		return 100
	}
	switch qp.limitSel.Selected {
	case "500 rows":
		return 500
	case "1000 rows":
		return 1000
	case "5000 rows (max)":
		return queryLimit
	default:
		return 100
	}
}

// run() is the "Run Current" button (TablePlus Run Current / Cancel
// dual-purpose): an active selection runs as-is, otherwise the statement
// containing the cursor runs — and gets highlighted first so the user sees
// exactly what will execute. While a run is in flight the button cancels
// instead of starting.
func (qp *queryPane) run() {
	if qp.running {
		qp.cancelRun()
		return
	}
	if qp.session() == nil {
		return
	}
	qp.hideAuto()
	text := qp.editor.Text
	if sel := strings.TrimSpace(qp.editor.SelectedText()); sel != "" {
		qp.runChain([]string{sel})
		return
	}
	start, end := qp.currentStatementRange()
	stmt := strings.TrimSpace(text[start:end])
	if stmt == "" {
		showInfo(qp.window, "Run", "The cursor is not inside a statement.")
		return
	}
	// Point the cursor at the start of the statement that will run (Fyne
	// v2.8 has no public SetSelection, so the cursor marks the highlight).
	qp.moveToByte(start)
	qp.runChain([]string{stmt})
}

// runAll executes every statement in the editor sequentially, stopping at the
// first error (TablePlus "Run All" with Return-on-Error off → we stop, since
// re-running a half-applied batch is user-hostile).
func (qp *queryPane) runAll() {
	if qp.running || qp.session() == nil {
		return
	}
	qp.hideAuto()
	stmts := splitStatementsForRun(qp.editor.Text)
	if len(stmts) == 0 {
		showInfo(qp.window, "Run All", "No statements to run.")
		return
	}
	qp.runChain(stmts)
}

// runChain walks the statements, prompting for any query-parameter values,
// sending each to the session and stopping on the first failure.
func (qp *queryPane) runChain(stmts []string) {
	qp.beginRun()
	qp.resetResultTabs() // a fresh run starts a new tab set
	qp.runChainAt(stmts, 0)
}

func (qp *queryPane) runChainAt(stmts []string, i int) {
	if i >= len(stmts) {
		qp.finishRun()
		return
	}
	stmt := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(stmts[i]), ";"))
	if stmt == "" {
		qp.runChainAt(stmts, i+1)
		return
	}
	// Query parameters: prompt (remembered in queryvars.json) before sending.
	if names := extractParams(stmt); len(names) > 0 {
		base := stmt
		qp.promptParams(names, func(vals map[string]string) {
			qp.sendStatement(substituteParams(base, vals), stmts, i)
		})
		return
	}
	qp.sendStatement(stmt, stmts, i)
}

// sendStatement sends one prepared statement, streams its result into the
// pane's grid and then continues (or stops) the chain.
func (qp *queryPane) sendStatement(stmt string, all []string, i int) {
	// Safe Mode gate (per the connection's level) before anything is sent;
	// the actual send runs as the gate's continuation. When the gate is
	// declined or the password check fails, finishRun resets the run state
	// so the panel does not stay stuck on "Cancel".
	gateSafeMode(qp.window, qp.conn, stmt, func() {
		qp.sendGated(stmt, all, i)
	}, func() {
		qp.finishRun()
	})
}

// sendGated sends one statement that already passed the Safe Mode gate.
func (qp *queryPane) sendGated(stmt string, all []string, i int) {
	// Auto-LIMIT plain SELECTs the user did not cap (TablePlus behaviour).
	stmt = autoLimit(stmt, qp.conn.Driver, qp.rowLimit())
	label := resultName(stmt)
	sess := qp.session()
	qp.logQuery("执行：%s", oneLine(stmt, 120))
	qp.timer.Text = fmt.Sprintf("running %d/%d…", i+1, len(all))
	qp.timer.Color = pal().statusFG
	qp.timer.Refresh()

	// Streaming path: fetch the result incrementally (bounded memory, paints
	// progressively). Sessions without the capability fall back to the
	// materialise-then-draw path below.
	if st, ok := db.AsStreamer(sess); ok {
		qp.runStreaming(st, stmt, label, all, i)
		return
	}

	safeGo("query.run", func() {
		// The grid is captured here, not re-read from resultSlot per batch: the
		// user can switch to an older run-result tab (or a pin) while batches
		// are still arriving, and the rows must land in the grid they were
		// fetched for, not in whatever is on screen at the moment.
		var g *dataTable
		var tab *queryResultTab // this statement's own tab; its meta lands here
		res, err := sess.Query(stmt, qp.rowLimit())
		// Build an empty grid on the UI thread, then stream the rows in so a
		// large result paints incrementally instead of freezing (TablePlus
		// TableContentV4.AppendData + generation-guarded session).
		safeDo(func() {
			if err != nil {
				qp.timer.Color = hex("#E53935")
				switch {
				case errors.Is(err, db.ErrQueryCancelled):
					qp.timer.Text = "cancelled"
					qp.logQuery("已取消：%s", oneLine(stmt, 80))
					qp.finishRun()
				case looksLikeTimeout(err):
					qp.timer.Text = "timeout"
					qp.logQuery("执行超时：%s", oneLine(stmt, 80))
					qp.finishRun()
					qp.showTimeoutDialog(stmt, all, i)
				default:
					if qp.continueOnError && i+1 < len(all) {
						// Surface the failure but keep the run going
						// (TablePlus "Return on Error" inverted).
						qp.timer.Text = fmt.Sprintf("stmt %d failed, continuing…", i+1)
						qp.timer.Refresh()
						qp.logQuery("执行失败（继续）：%v", err)
						// 不调 finishRun：链还要继续，running 必须保持 true，
						// 否则下一条语句执行期间面板可被再次点 Run 并发。
						// 直接在 UI 线程 Show：Fyne 2.8 的 overlay/窗口操作不许离开主线程，
						// 裸 goroutine 里的空 recover 还会把 panic 无声吞掉（绕过 crash.go 的全量记录）。
						dialog.NewError(friendlyErr(err), qp.window).Show()
						qp.runChainAt(all, i+1)
						return
					}
					qp.timer.Text = "error"
					qp.timer.Refresh()
					qp.logQuery("执行失败：%v", err)
					qp.finishRun()
					dialog.NewError(friendlyErr(err), qp.window).Show()
				}
				return
			}
			cols := make([]ColumnInfo, 0, len(res.Columns))
			for _, c := range res.Columns {
				cols = append(cols, ColumnInfo{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Key: c.Key})
			}
			g = newDataTable(&TableData{Cols: cols, Fetched: 0, Total: -1, Approx: false}, qp.status)
			g.window = qp.window
			g.tableName = "" // query results have no owning table
			g.onPin = qp.pinResult
			g.onRowSelect = func(idx int) {
				if qp.onRowSelect != nil {
					qp.onRowSelect(g.data, idx)
				}
			}
			qp.activePin = -1 // a fresh live run supersedes any pinned view
			qp.resultSlot.Objects = []fyne.CanvasObject{g}
			qp.resultSlot.Refresh()
			qp.bindFind(g)
			qp.updateResultChrome()
			qp.status.setQueryInfo(qp.resultStatusLine(label, len(cols), "执行中…"))
			tab = qp.recordResultTab(i, label, stmt, g)
		})
		if err != nil {
			return
		}
		total := len(res.Rows)
		myGen := qp.bumpRun()
		// Convert every cell to its display string once, up front. Calling
		// StringRows() inside the batch loop re-converted the whole result
		// set per batch (5× the work and garbage at rowLimit=5000) and kept
		// one full copy of the rows alive per chunk.
		rows := res.StringRows()
		res.Rows = nil // release the CellPayload copy; the grid owns rows now
		const batch = 1000
		for off := 0; off < total; off += batch {
			end := off + batch
			if end > total {
				end = total
			}
			chunk := rows[off:end]
			safeDo(func() {
				if myGen != int(qp.runGen.Load()) {
					return // a newer run superseded this one
				}
				if g == nil {
					return
				}
				g.appendRows(chunk)
				qp.status.setRows(g.data.Fetched, total, false)
			})
		}
		safeDo(func() {
			if myGen != int(qp.runGen.Load()) {
				return
			}
			noun := "rows"
			if res.RowsAffected > 0 && total == 0 {
				noun = "rows affected"
			}
			affected := max64(res.RowsAffected, int64(total))
			timerText := fmt.Sprintf("%d ms · %d %s", res.DurationMS, affected, noun)
			statusLine := qp.resultStatusLine(label, len(res.Columns),
				truncationNote(fmt.Sprintf("%d %s", total, noun), res.Truncated))
			qp.timer.Text = timerText
			qp.timer.Color = pal().statusFG
			qp.timer.Refresh()
			qp.logQuery("完成：%d %s · %d ms", affected, noun, res.DurationMS)
			qp.status.setQueryInfo(statusLine)
			tab.setMeta(timerText, statusLine)
			if qp.side != nil {
				qp.side.appendHistory(stmt)
			}
			if i+1 < len(all) {
				qp.runChainAt(all, i+1)
			} else {
				qp.finishRun()
			}
		})
	})
}

// finishRun resets the busy state and the Run/Cancel button.
func (qp *queryPane) finishRun() {
	qp.running = false
	qp.runBtn.setLabel("Run Current")
}

// beginRun arms the run state (running flag + Cancel label) without resetting
// the result tabs. Used by runChain (which also resets tabs separately) and by
// the timeout-dialog retry/reconnect callbacks, where a statement is re-sent
// under the same run and must not appear as a fresh run.
func (qp *queryPane) beginRun() {
	// FQ-02: a transparently rebuilt plugin session means the server
	// connection was re-opened — transactions, temp tables and session
	// variables from earlier statements are gone. Say so once
	// (acknowledge-on-read), right where the next statement is about to run.
	if o, ok := db.AsRebuildObserver(qp.session()); ok && o.TakeRebuilt() {
		qp.logQuery("插件进程已重建：连接已重新打开，此前的事务/临时表/会话变量已丢失")
		qp.status.setQueryInfo("⚠ 插件进程已重建——服务器端会话状态已丢失")
	}
	qp.running = true
	qp.runBtn.setLabel("Cancel")
}

// streamBatch is how many rows the streaming path asks for per frame.
const streamBatch = 500

// streamState holds the per-run streaming UI state. Its fields are only touched
// on the UI thread (inside safeDo), so no locking is needed.
type streamState struct {
	started bool
	g       *dataTable
	tab     *queryResultTab // this statement's own tab; its meta lands here
	rows    int64
}

// runStreaming fetches a statement's result incrementally via the session's
// Streamer, painting each batch into the grid as it arrives. It mirrors the
// non-streaming path (generation guard, error handling, chain continuation) so
// the two behave identically from the user's point of view.
func (qp *queryPane) runStreaming(st db.Streamer, stmt, label string, all []string, i int) {
	safeGo("query.stream", func() {
		myGen := qp.bumpRun()
		state := &streamState{}
		limit := qp.rowLimit()
		start := time.Now()
		var seen int64
		res, err := st.QueryStream(stmt, streamBatch, func(pcols []proto.ColumnInfo, rows [][]*string) (bool, error) {
			// Count on this (background) goroutine so the row cap is enforced
			// synchronously; safeDo only schedules the paint.
			seen += int64(len(rows))
			keep := int(seen) < limit
			safeDo(func() {
				if myGen != int(qp.runGen.Load()) {
					return
				}
				if g := qp.streamGrid(state, pcols, label, i, stmt); g != nil {
					g.appendRows(rows)
					state.rows = seen
					qp.status.setRows(g.data.Fetched, int(seen), false)
				}
			})
			return keep, nil
		})
		// A capped stream returns no aggregate from the host; fall back to the
		// locally measured elapsed time so the status line still shows a figure.
		if res.DurationMS == 0 {
			res.DurationMS = time.Since(start).Milliseconds()
		}
		// The host's stream path returns no aggregate, so the flag has to be
		// inferred: an overshooting frame (more rows than the pane's limit
		// arrived in one batch) proves truncation. Exactly `limit` rows is
		// ambiguous — the stream stops either way — so it stays unclaimed
		// rather than inventing a flag.
		res.Truncated = seen > int64(limit)
		safeDo(func() {
			if myGen != int(qp.runGen.Load()) {
				return
			}
			qp.finishStream(state, res, err, stmt, label, all, i)
		})
	})
}

// streamGrid lazily builds the result grid on the first emitted frame (so the
// column metadata arrives with the data) and returns it for appending.
func (qp *queryPane) streamGrid(state *streamState, pcols []proto.ColumnInfo, label string, i int, stmt string) *dataTable {
	if state.started {
		return state.g
	}
	state.started = true
	cols := make([]ColumnInfo, 0, len(pcols))
	for _, c := range pcols {
		cols = append(cols, ColumnInfo{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Key: c.Key})
	}
	g := newDataTable(&TableData{Cols: cols, Fetched: 0, Total: -1, Approx: false}, qp.status)
	g.window = qp.window
	g.tableName = "" // query results have no owning table
	g.onPin = qp.pinResult
	g.onRowSelect = func(idx int) {
		if qp.onRowSelect != nil {
			qp.onRowSelect(g.data, idx)
		}
	}
	qp.activePin = -1 // a fresh live run supersedes any pinned view
	qp.resultSlot.Objects = []fyne.CanvasObject{g}
	qp.resultSlot.Refresh()
	qp.bindFind(g)
	qp.updateResultChrome()
	qp.status.setQueryInfo(qp.resultStatusLine(label, len(cols), "执行中…"))
	state.tab = qp.recordResultTab(i, label, stmt, g)
	state.g = g
	return g
}

// finishStream finalises a streaming run: surfaces errors (mirroring the
// non-streaming path), or shows the row/affected count and continues the chain.
func (qp *queryPane) finishStream(state *streamState, res proto.QueryResult, err error, stmt, label string, all []string, i int) {
	if err != nil {
		qp.timer.Color = hex("#E53935")
		switch {
		case errors.Is(err, db.ErrQueryCancelled):
			qp.timer.Text = "cancelled"
			qp.logQuery("已取消：%s", oneLine(stmt, 80))
			qp.finishRun()
		case looksLikeTimeout(err):
			qp.timer.Text = "timeout"
			qp.logQuery("执行超时：%s", oneLine(stmt, 80))
			qp.showTimeoutDialog(stmt, all, i)
		default:
			if qp.continueOnError && i+1 < len(all) {
				qp.timer.Text = fmt.Sprintf("stmt %d failed, continuing…", i+1)
				qp.timer.Refresh()
				qp.logQuery("执行失败（继续）：%v", err)
				// 直接在 UI 线程 Show：Fyne 2.8 的 overlay/窗口操作不许离开主线程，
				// 裸 goroutine 里的空 recover 还会把 panic 无声吞掉（绕过 crash.go 的全量记录）。
				dialog.NewError(friendlyErr(err), qp.window).Show()
				qp.runChainAt(all, i+1)
				return
			}
			qp.timer.Text = "error"
			qp.timer.Refresh()
			qp.logQuery("执行失败：%v", err)
			dialog.NewError(friendlyErr(err), qp.window).Show()
		}
		qp.finishRun()
		return
	}

	// A non-row-returning statement emits no frame, so ensure a grid exists to
	// display the "rows affected" outcome.
	if !state.started {
		g := newDataTable(&TableData{Fetched: 0, Total: -1}, qp.status)
		g.window = qp.window
		g.tableName = ""
		qp.activePin = -1
		qp.resultSlot.Objects = []fyne.CanvasObject{g}
		qp.resultSlot.Refresh()
		qp.bindFind(g)
		qp.updateResultChrome()
		state.tab = qp.recordResultTab(i, label, stmt, g)
		state.g = g
		state.started = true
	}

	total := state.rows
	noun := "rows"
	if res.RowsAffected > 0 && total == 0 {
		noun = "rows affected"
	}
	affected := max64(res.RowsAffected, total)
	timerText := fmt.Sprintf("%d ms · %d %s", res.DurationMS, affected, noun)
	statusLine := qp.resultStatusLine(label, len(state.g.data.Cols),
		truncationNote(fmt.Sprintf("%d %s", total, noun), res.Truncated))
	qp.timer.Text = timerText
	qp.timer.Color = pal().statusFG
	qp.timer.Refresh()
	qp.logQuery("完成：%d %s · %d ms", affected, noun, res.DurationMS)
	qp.status.setQueryInfo(statusLine)
	state.tab.setMeta(timerText, statusLine)
	if qp.side != nil {
		qp.side.appendHistory(stmt)
	}
	if i+1 < len(all) {
		qp.runChainAt(all, i+1)
	} else {
		qp.finishRun()
	}
}

// cancelRun asks the session to kill the in-flight statement (db.Canceller);
// for sessions without the capability it is a no-op with a notice.
func (qp *queryPane) cancelRun() {
	c, ok := db.AsCanceller(qp.session())
	if !ok {
		showInfo(qp.window, "Cancel", "This connection does not support cancelling queries.")
		return
	}
	safeGo("query.cancel", func() {
		if err := c.KillQuery(); err != nil {
			safeDo(func() { showInfo(qp.window, "Cancel", err.Error()) })
		}
	})
}

// looksLikeTimeout reports whether the error is of the "server too slow"
// class that deserves the wait/reconnect dialog (TablePlus behaviour).
// Structured checks come first (context deadline, net.Error timeout); the
// string fallback covers driver-specific messages. "eof" is deliberately not
// treated as a timeout: a dropped connection is not the server being slow and
// must not open the wait dialog.
func looksLikeTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, m := range []string{"timeout", "timed out", "deadline"} {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// currentStatementRange returns the byte [start,end) of the statement
// containing the cursor (skipping to the next non-blank statement when the
// cursor sits in whitespace between statements) — the "highlight current
// query" readout.
// splitStatementsForRun breaks a script into non-blank top-level statements,
// honouring quoted strings, -- line comments and /* */ blocks. A single
// trailing ';' on each statement is stripped; attached leading comments are
// kept (they are valid SQL).
func splitStatementsForRun(script string) []string {
	var out []string
	for _, r := range sqlscan.StatementRanges(script) {
		s := strings.TrimSpace(script[r.Start:r.End])
		s = strings.TrimSuffix(s, ";")
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (qp *queryPane) currentStatementRange() (int, int) {
	if qp.editor == nil {
		return 0, 0
	}
	text := qp.editor.Text
	r := []rune(text)
	cursor := runeOffset(r, qp.editor.CursorRow, qp.editor.CursorColumn)
	if cursor > len(r) {
		cursor = len(r)
	}
	cursorByte := len(string(r[:cursor]))
	ranges := sqlscan.StatementRanges(text)
	for i, rg := range ranges {
		if cursorByte >= rg.Start && cursorByte < rg.End {
			if strings.TrimSpace(text[rg.Start:rg.End]) == "" {
				for _, nx := range ranges[i+1:] {
					if strings.TrimSpace(text[nx.Start:nx.End]) != "" {
						return nx.Start, nx.End
					}
				}
			}
			return rg.Start, rg.End
		}
	}
	if len(ranges) > 0 {
		rg := ranges[len(ranges)-1]
		return rg.Start, rg.End
	}
	return 0, 0
}

// showTimeoutDialog offers Wait & Retry and Reconnect & Retry after a query
// times out (TablePlus "The server is taking too long").
func (qp *queryPane) showTimeoutDialog(stmt string, all []string, i int) {
	// Both actions hide the dialog first: without this it stayed on screen
	// under the reconnect progress box and after the retry re-ran, and the
	// user had to close it by hand.
	var d *dialog.CustomDialog
	retry := widget.NewButton("等待并重试", func() {
		d.Hide()
		qp.beginRun() // 重发同一条语句：恢复 running，避免重试期间再点 Run 并发
		qp.sendStatement(stmt, all, i)
	})
	reconnect := widget.NewButton("重连并重试", func() {
		d.Hide()
		if qp.onReconnect == nil {
			return
		}
		qp.onReconnect(func(sess db.Session) {
			if sess != nil {
				qp.beginRun() // 会话就绪后立即武装，不等 sendStatement 内部
				qp.sendStatement(stmt, all, i)
			}
		})
	})
	body := container.NewVBox(
		widget.NewLabel("The server is taking too long to respond."),
		container.NewHBox(retry, reconnect),
	)
	d = dialog.NewCustom("查询超时", "", body, qp.window)
	d.Resize(fyne.NewSize(420, 160))
	d.Show()
}

// resultStatusLine is the bottom "● …" line of a query result (TablePlus:
// "● Query result — N columns · M rows"), optionally carrying the "-- name:"
// label the author put on the statement.
func (qp *queryPane) resultStatusLine(label string, cols int, detail string) string {
	name := label
	if name == "" {
		name = "Query result"
	}
	return fmt.Sprintf("%s — %d columns · %s", name, cols, detail)
}

// truncationNote flags a result that is a window rather than the whole set. A
// capped read used to pose its first page as the complete result; the flag
// (set by sqlkit.ReadRows, inferred on the stream path) is what lets the status
// line say so instead.
func truncationNote(detail string, truncated bool) string {
	if !truncated {
		return detail
	}
	return detail + " · truncated"
}

// promptParams opens the Variables dialog (TablePlus) for the named
// placeholders: one entry per parameter, pre-filled from the remembered
// values. Apply saves them for next time and hands them to onApply;
// Cancel just dismisses the dialog.
func (qp *queryPane) promptParams(names []string, onApply func(map[string]string)) {
	memory := loadQueryVars()
	entries := make([]*widget.Entry, len(names))
	form := container.NewVBox()
	for i, n := range names {
		e := widget.NewEntry()
		e.SetText(memory[n])
		e.SetPlaceHolder("value for " + n + " (quote strings yourself, e.g. 'shipped')")
		entries[i] = e
		form.Objects = append(form.Objects,
			container.NewBorder(nil, nil, widget.NewLabel(n), nil, e))
	}

	var dlg dialog.Dialog
	apply := widget.NewButton("Apply", func() {
		vals := make(map[string]string, len(names))
		for i, n := range names {
			vals[n] = entries[i].Text
			memory[n] = vals[n] // remember for the next run
		}
		saveQueryVars(memory)
		dlg.Hide()
		if onApply != nil {
			onApply(vals)
		}
	})
	apply.Importance = widget.HighImportance
	cancel := widget.NewButton("Cancel", func() {
		dlg.Hide()
		qp.finishRun() // Cancel 放弃本次链：不调会卡在 "Cancel"（与 gateSafeMode 拒绝同理）
	})

	body := container.NewBorder(
		widget.NewLabel("Query parameters — values are inserted as written:"),
		container.NewHBox(layout.NewSpacer(), cancel, apply),
		nil, nil,
		container.NewScroll(form),
	)
	dlg = dialog.NewCustom("Query parameters", "", body, qp.window)
	dlg.Resize(fyne.NewSize(440, 300))
	dlg.Show()
}

func (qp *queryPane) beautify() {
	text := strings.TrimSpace(qp.editor.Text)
	if text == "" {
		return
	}
	qp.editor.SetText(formatSQL(text))
}

// accentButton: the blue "Run Current" style button.
type accentButton struct {
	widget.BaseWidget
	label   string
	onTap   func()
	enabled bool
}

func newAccentButton(label string) *accentButton {
	b := &accentButton{label: label, enabled: true}
	b.ExtendBaseWidget(b)
	return b
}

// SetEnabled toggles the busy state of the Run button.
func (b *accentButton) SetEnabled(e bool) {
	if b.enabled != e {
		b.enabled = e
		b.Refresh()
	}
}

// setLabel changes the button caption (Run Current / Cancel) and repaints.
func (b *accentButton) setLabel(s string) {
	if b.label != s {
		b.label = s
		b.Refresh()
	}
}

func (b *accentButton) Tapped(*fyne.PointEvent) {
	if b.enabled && b.onTap != nil {
		b.onTap()
	}
}

func (b *accentButton) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(pal().accent)
	bg.CornerRadius = 6
	text := canvas.NewText(b.label, colorWhite)
	text.TextSize = 12
	text.TextStyle.Bold = true
	return &accentBtnRenderer{b: b, bg: bg, text: text}
}

type accentBtnRenderer struct {
	b    *accentButton
	bg   *canvas.Rectangle
	text *canvas.Text
}

func (r *accentBtnRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.text}
}

func (r *accentBtnRenderer) MinSize() fyne.Size {
	return fyne.NewSize(r.text.MinSize().Width+22, 26)
}

func (r *accentBtnRenderer) Destroy() {}

func (r *accentBtnRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	t := r.text.MinSize()
	r.text.Move(fyne.NewPos((size.Width-t.Width)/2, (size.Height-t.Height)/2))
}

func (r *accentBtnRenderer) Refresh() {
	p := pal()
	r.bg.FillColor = p.accent
	r.text.Color = colorWhite
	r.text.Text = r.b.label
	if !r.b.enabled {
		r.bg.FillColor = p.segmentBG
		r.text.Color = p.segmentFG
	}
	r.bg.Refresh()
	r.text.Refresh()
}

// structurePane lists the columns of a table (Structure segment).
type structurePane struct {
	widget.BaseWidget
	data *TableData
}

func newStructurePane(data *TableData) *structurePane {
	sp := &structurePane{data: data}
	sp.ExtendBaseWidget(sp)
	return sp
}

func (sp *structurePane) CreateRenderer() fyne.WidgetRenderer {
	header := container.NewGridWithColumns(4,
		boldLabel("Field"), boldLabel("Type"), boldLabel("Null"), boldLabel("Key"),
	)
	rowsBox := container.NewVBox()
	for _, c := range sp.data.Cols {
		rowsBox.Objects = append(rowsBox.Objects,
			container.NewGridWithColumns(4,
				plainLabel(c.Name),
				plainLabel(c.Type),
				plainLabel(map[bool]string{true: "YES", false: "NO"}[c.Nullable]),
				plainLabel(c.Key),
			))
	}
	list := container.NewVBox(header, widget.NewSeparator(), rowsBox)
	scroll := container.NewScroll(list)
	return widget.NewSimpleRenderer(scroll)
}

func boldLabel(s string) *canvas.Text {
	t := canvas.NewText(s, pal().gridHeaderFG)
	t.TextSize = 12
	t.TextStyle.Bold = true
	return t
}

func plainLabel(s string) *canvas.Text {
	t := canvas.NewText(s, pal().gridCellFG)
	t.TextSize = 12
	return t
}

// max64 returns the larger of two int64 values.
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
