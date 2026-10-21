package main

// Query tab: SQL editor with beautify/run buttons and a results grid.
// Structure pane: column list for the Structure segment.

import (
	"errors"
	"fmt"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// colorWhite is used for text on accent backgrounds.
var colorWhite = color.RGBA{R: 255, G: 255, B: 255, A: 255}

// queryLimit caps interactive query results.
const queryLimit = 5000

// queryPane is the SQL tab content.
type queryPane struct {
	widget.BaseWidget
	editor     *widget.Entry
	resultSlot *fyne.Container // holds the current result grid (replaced per run)
	status     *statusBar
	timer      *canvas.Text
	posLabel   *widget.Label // "第 1 行, 第 1 列, 位置 0"
	limitSel   *widget.Select
	runBtn     *accentButton
	window     fyne.Window
	conn       Connection
	sess       db.Session
	side       *sidebar
	running    bool

	// sessFn returns the workspace's current session. The workspace re-dials
	// (and closes the old session) when the user picks a database, so the pane
	// must not keep a stale session captured at build time.
	sessFn        func() db.Session
	onReconnect   func() db.Session              // workspace dials a fresh session (timeout dialog)
	onRowSelect   func(data *TableData, row int) // a result row was selected
	colCache      map[string][]string            // table -> column names (completion)
	shortcutBound bool

	// runGen discards result batches from a superseded run (TablePlus
	// generation guard). bumpRun returns the new generation.
	runGen int
	// continueOnError keeps a multi-statement run going past failures
	// (TablePlus "Return on Error" inverted; we default to stopping).
	continueOnError bool

	// pinned results (TablePlus "Pin result"): snapshots that survive re-runs,
	// shown as a chip row above the live grid.
	pins      []pinnedResult
	pinBar    *fyne.Container
	activePin int // index of the pin currently shown in resultSlot (-1 = live)
	// find box + counter for in-result search on this pane's result grid.
	findEntry   *gridSearchEntry
	findCounter *widget.Label
}

func newQueryPane(w fyne.Window, c Connection, sess db.Session, side *sidebar) *queryPane {
	qp := &queryPane{window: w, conn: c, sess: sess, side: side}
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

func (qp *queryPane) CreateRenderer() fyne.WidgetRenderer {
	qp.editor = widget.NewMultiLineEntry()
	qp.editor.SetPlaceHolder("Write SQL here, e.g.\nSELECT id, name FROM users LIMIT 20;\n\nHint: Ctrl+Space completes keywords, tables and columns.")
	qp.editor.SetText("SELECT *\nFROM users\nLIMIT 20;")
	// keep the cursor-position readout roughly current
	qp.editor.OnChanged = func(string) { qp.refreshPos() }

	qp.posLabel = widget.NewLabel("Line 1, Col 1, Pos 0")
	qp.limitSel = widget.NewSelect([]string{"100 rows", "500 rows", "1000 rows", "No limit"}, func(string) {})
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
	contErr := widget.NewCheck("Continue on error", func(on bool) { qp.continueOnError = on })

	// toolbar sits below the editor (TablePlus SQL editor): position | limit |
	// Beautify | Complete | Run All | Run Current (doubles as Cancel) | timer
	toolbar := container.NewBorder(nil, nil,
		qp.posLabel,
		container.NewHBox(qp.limitSel, spacerW(10), beautify, spacerW(6), completeBtn, spacerW(6),
			runAll, spacerW(6), contErr, spacerW(6), runBtn, spacerW(10), qp.timer),
		layout.NewSpacer(),
	)
	editorPane := container.NewBorder(nil, toolbar, nil, nil, container.NewPadded(qp.editor))

	qp.status = newStatusBar()
	qp.resultSlot = container.NewStack(newDataTable(emptyTableData(), qp.status))

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
		widget.NewIcon(theme.SearchIcon()),
		qp.findEntry,
		qp.findCounter,
	)

	split := container.NewVSplit(editorPane, container.NewBorder(nil, qp.status, nil, nil, qp.resultSlot))
	split.SetOffset(0.5)

	// pinned-result chip row (TablePlus "Pin result"), shown only when there
	// are pins. Clicking a chip shows that snapshot; its × un-pins it.
	qp.pinBar = container.NewHBox()
	qp.pinBar.Hide()

	// search bar sits just above the result grid
	searchWrap := container.NewBorder(nil, nil, nil, nil, findBox)
	resultArea := container.NewBorder(nil, nil, nil, nil, split)
	resultArea = container.NewVBox(qp.pinBar, searchWrap, resultArea)

	qp.bindCompletionShortcut()
	return widget.NewSimpleRenderer(resultArea)
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
	g.tableName = ""
	g.onRowSelect = func(idx int) {
		if qp.onRowSelect != nil {
			qp.onRowSelect(g.data, idx)
		}
	}
	qp.activePin = i
	qp.resultSlot.Objects = []fyne.CanvasObject{g}
	qp.resultSlot.Refresh()
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
		chip := newFlatButton("📌 " + name)
		chip.onTap = func() { qp.showPin(i) }
		un := newFlatButton("✕")
		un.onTap = func() { qp.unpin(i) }
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
	qp.runGen++
	return qp.runGen
}

// refreshPos updates the "第 N 行, 第 M 列, 位置 K" readout from the editor text.
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
	case "No limit":
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
	qp.running = true
	qp.runBtn.setLabel("Cancel")
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
	// the actual send runs as the gate's continuation.
	gateSafeMode(qp.window, qp.conn, stmt, func() {
		qp.sendGated(stmt, all, i)
	})
}

// sendGated sends one statement that already passed the Safe Mode gate.
func (qp *queryPane) sendGated(stmt string, all []string, i int) {
	// Auto-LIMIT plain SELECTs the user did not cap (TablePlus behaviour).
	stmt = autoLimit(stmt, qp.conn.Driver, qp.rowLimit())
	label := resultName(stmt)
	sess := qp.session()
	qp.timer.Text = fmt.Sprintf("running %d/%d…", i+1, len(all))
	qp.timer.Color = pal().statusFG
	qp.timer.Refresh()

	safeGo("query.run", func() {
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
					qp.finishRun()
				case looksLikeTimeout(err):
					qp.timer.Text = "timeout"
					qp.finishRun()
					qp.showTimeoutDialog(stmt, all, i)
				default:
					if qp.continueOnError && i+1 < len(all) {
						// Surface the failure but keep the run going
						// (TablePlus "Return on Error" inverted).
						qp.timer.Text = fmt.Sprintf("stmt %d failed, continuing…", i+1)
						qp.timer.Refresh()
						qp.finishRun()
						go func() {
							defer func() { recover() }()
							dialog.NewError(err, qp.window).Show()
						}()
						qp.runChainAt(all, i+1)
						return
					}
					qp.timer.Text = "error"
					qp.timer.Refresh()
					qp.finishRun()
					dialog.NewError(err, qp.window).Show()
				}
				return
			}
			cols := make([]ColumnInfo, 0, len(res.Columns))
			for _, c := range res.Columns {
				cols = append(cols, ColumnInfo{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Key: c.Key})
			}
			g := newDataTable(&TableData{Cols: cols, Fetched: 0, Total: -1, Approx: false}, qp.status)
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
			qp.status.setQueryInfo(qp.resultStatusLine(label, len(cols), "执行中…"))
		})
		if err != nil {
			return
		}
		total := len(res.Rows)
		myGen := qp.bumpRun()
		const batch = 1000
		for off := 0; off < total; off += batch {
			end := off + batch
			if end > total {
				end = total
			}
			chunk := res.Rows[off:end]
			// yield so the UI thread can paint the previous batch rather
			// than freezing on one giant rebuild.
			if off > 0 {
				time.Sleep(time.Millisecond)
			}
			safeDo(func() {
				if myGen != qp.runGen {
					return // a newer run superseded this one
				}
				g := qp.resultGrid()
				if g == nil {
					return
				}
				g.appendRows(chunk)
				qp.status.setRows(g.data.Fetched, total, false)
			})
		}
		safeDo(func() {
			if myGen != qp.runGen {
				return
			}
			noun := "rows"
			if res.RowsAffected > 0 && total == 0 {
				noun = "rows affected"
			}
			qp.timer.Text = fmt.Sprintf("%d ms · %d %s", res.DurationMS, max64(res.RowsAffected, int64(total)), noun)
			qp.timer.Color = pal().statusFG
			qp.timer.Refresh()
			qp.status.setQueryInfo(qp.resultStatusLine(label, len(res.Columns),
				fmt.Sprintf("%d %s", total, noun)))
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

// cancelRun asks the session to kill the in-flight statement (db.Canceller);
// for sessions without the capability it is a no-op with a notice.
func (qp *queryPane) cancelRun() {
	c, ok := qp.session().(db.Canceller)
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
func looksLikeTimeout(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, m := range []string{"timeout", "timed out", "deadline", "i/o timeout", "eof"} {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// stmtRanges is the [start,end) byte span of one top-level statement.
type stmtRanges struct{ start, end int }

// splitStatementsForRun breaks a script into non-blank top-level statements,
// honouring quoted strings, -- line comments and /* */ blocks (the backup.go
// splitStatements variant does not skip block comments, which is too lossy
// for an "Run All" executor). A single trailing ';' on each statement is
// stripped; attached leading comments are kept (they are valid SQL).
func splitStatementsForRun(script string) []string {
	var out []string
	for _, r := range topLevelStatementRanges(script) {
		s := strings.TrimSpace(script[r.start:r.end])
		s = strings.TrimSuffix(s, ";")
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// topLevelStatementRanges scans a script honouring quoted strings, -- line
// comments and /* */ blocks, returning the byte span of every top-level
// statement (including any trailing span after the last ';').
func topLevelStatementRanges(script string) []stmtRanges {
	var out []stmtRanges
	start := 0
	var q byte // active quote char (0 = none)
	inLine, inBlock := false, false
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
			}
		case inBlock:
			if c == '*' && i+1 < len(script) && script[i+1] == '/' {
				inBlock = false
				i++
			}
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '\'' || c == '"':
			q = c
		case c == '-' && i+1 < len(script) && script[i+1] == '-':
			inLine = true
			i++
		case c == '/' && i+1 < len(script) && script[i+1] == '*':
			inBlock = true
			i++
		case c == ';':
			out = append(out, stmtRanges{start, i + 1})
			start = i + 1
		}
	}
	out = append(out, stmtRanges{start, len(script)})
	return out
}

// currentStatementRange returns the byte [start,end) of the statement
// containing the cursor (skipping to the next non-blank statement when the
// cursor sits in whitespace between statements) — the "highlight current
// query" readout.
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
	ranges := topLevelStatementRanges(text)
	for i, rg := range ranges {
		if cursorByte >= rg.start && cursorByte < rg.end {
			if strings.TrimSpace(text[rg.start:rg.end]) == "" {
				for _, nx := range ranges[i+1:] {
					if strings.TrimSpace(text[nx.start:nx.end]) != "" {
						return nx.start, nx.end
					}
				}
			}
			return rg.start, rg.end
		}
	}
	if len(ranges) > 0 {
		rg := ranges[len(ranges)-1]
		return rg.start, rg.end
	}
	return 0, 0
}

// showTimeoutDialog offers Wait & Retry and Reconnect & Retry after a query
// times out (TablePlus "The server is taking too long").
func (qp *queryPane) showTimeoutDialog(stmt string, all []string, i int) {
	retry := widget.NewButton("等待并重试", func() {
		qp.sendStatement(stmt, all, i)
	})
	reconnect := widget.NewButton("重连并重试", func() {
		if qp.onReconnect == nil {
			return
		}
		if sess := qp.onReconnect(); sess != nil {
			qp.sendStatement(stmt, all, i)
		}
	})
	body := container.NewVBox(
		widget.NewLabel("The server is taking too long to respond."),
		container.NewHBox(retry, reconnect),
	)
	d := dialog.NewCustom("查询超时", "", body, qp.window)
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
	cancel := widget.NewButton("Cancel", func() { dlg.Hide() })

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
	lines := strings.Split(qp.editor.Text, "\n")
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		out = append(out, strings.ToUpper(t))
	}
	qp.editor.SetText(strings.Join(out, "\n"))
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
	return fyne.NewSize(r.text.MinSize().Width+24, 28)
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
