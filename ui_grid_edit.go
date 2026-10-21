package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// Grid edit state. The queue holds raw strings; "" means NULL (the edit
// dialog's hint says so). Statements are only rendered at save time by
// changes.go, so nothing here can inject SQL.

// gridEditState collects the unsaved edits on top of the last result: cell
// updates keyed by row then column, deleted rows, and queued new rows.
type gridEditState struct {
	updates map[int]map[string]string
	// nulls marks which queued values mean SQL NULL. The queue stores
	// strings, where "" on its own already means NULL - so writing an empty
	// STRING (a perfectly good value in most engines) needs the distinction.
	nulls   map[int]map[string]bool
	deletes map[int]bool
	inserts []map[string]string
}

func newGridEditState() *gridEditState {
	return &gridEditState{
		updates: make(map[int]map[string]string),
		nulls:   make(map[int]map[string]bool),
		deletes: make(map[int]bool),
	}
}

func (g *gridEditState) any() bool {
	return g != nil && (len(g.updates) > 0 || len(g.deletes) > 0 || len(g.inserts) > 0)
}

// clone deep-copies the queue data (not the undo/redo stacks) so a snapshot can
// be restored later without aliasing the live maps.
func (g *gridEditState) clone() *gridEditState {
	n := newGridEditState()
	for r, cols := range g.updates {
		m := make(map[string]string, len(cols))
		for k, v := range cols {
			m[k] = v
		}
		n.updates[r] = m
	}
	for r, cols := range g.nulls {
		m := make(map[string]bool, len(cols))
		for k, v := range cols {
			m[k] = v
		}
		n.nulls[r] = m
	}
	for r := range g.deletes {
		n.deletes[r] = true
	}
	for _, ins := range g.inserts {
		m := make(map[string]string, len(ins))
		for k, v := range ins {
			m[k] = v
		}
		n.inserts = append(n.inserts, m)
	}
	return n
}

// gridUndoLimit caps the undo history so a long editing session cannot grow it
// without bound.
const gridUndoLimit = 100

// pushUndoSnapshot records the current queue before a mutating action so Ctrl+Z
// can bring it back. It also clears redo: a fresh edit invalidates anything that
// was undone-and-redoable.
func (c *Client) pushUndoSnapshot() {
	if c.grid.edits == nil {
		return
	}
	c.grid.undoStack = append(c.grid.undoStack, c.grid.edits.clone())
	if len(c.grid.undoStack) > gridUndoLimit {
		c.grid.undoStack = c.grid.undoStack[len(c.grid.undoStack)-gridUndoLimit:]
	}
	c.grid.redoStack = nil
}

// undoGridEdit rolls the pending queue back one step. Because edits are staged
// in memory until 保存, this is a pure in-memory rollback - no database
// rollback is involved.
func (c *Client) undoGridEdit() {
	if len(c.grid.undoStack) == 0 {
		c.setStatus("没有可撤销的改动")
		return
	}
	c.grid.redoStack = append(c.grid.redoStack, c.grid.edits.clone())
	last := c.grid.undoStack[len(c.grid.undoStack)-1]
	c.grid.undoStack = c.grid.undoStack[:len(c.grid.undoStack)-1]
	c.grid.edits = last
	c.afterEditQueued()
}

// redoGridEdit replays the step undoGridEdit took back.
func (c *Client) redoGridEdit() {
	if len(c.grid.redoStack) == 0 {
		c.setStatus("没有可重做的改动")
		return
	}
	c.grid.undoStack = append(c.grid.undoStack, c.grid.edits.clone())
	last := c.grid.redoStack[len(c.grid.redoStack)-1]
	c.grid.redoStack = c.grid.redoStack[:len(c.grid.redoStack)-1]
	c.grid.edits = last
	c.afterEditQueued()
}

// clearGridUndo drops both stacks; called when a new result replaces the queue
// (and after a save), so stale snapshots from a previous result never come back.
func (c *Client) clearGridUndo() {
	c.grid.undoStack = nil
	c.grid.redoStack = nil
}

// setUpdate queues one cell value for an existing row; an empty value means
// NULL. A repeat edit of the same cell replaces the queued one.
func (g *gridEditState) setUpdate(row int, column, value string) {
	cols, ok := g.updates[row]
	if !ok {
		cols = make(map[string]string)
		g.updates[row] = cols
	}
	cols[column] = value
}

func (g *gridEditState) updateAt(row int, column string) (string, bool) {
	if g == nil {
		return "", false
	}
	v, ok := g.updates[row][column]
	return v, ok
}

func (g *gridEditState) isDeleted(row int) bool {
	return g != nil && g.deletes[row]
}

// setInsertEdit queues a cell value on a queued new row; i is the index into
// the inserts slice.
func (g *gridEditState) setInsertEdit(i int, column, value string) {
	g.inserts[i][column] = value
}

// setCellValue queues one cell change without a dialog: the entry point behind
// the details pane's 设为 NULL / 设为空串, and what a programmatic edit needs.
// An empty value means NULL; that is the queue's own convention, so a column can
// be set to NULL but not to an empty string. The limit predates this entry point
// and stays: redefining "" is a write-path change, not a polish.
//
// It reports the reason it refused, or "" when the edit was queued.
func (c *Client) setCellValue(row, col int, value string) string {
	if !c.gridEditable() {
		return "该结果不可编辑（编辑需 SELECT * 单表）"
	}
	if col < 0 || col >= len(c.grid.cols) {
		return "该列不在这个结果里"
	}
	if c.grid.edits.isDeleted(row) {
		return "该行已标记删除，先取消删除再改值"
	}
	if row < len(c.grid.rows) {
		c.pushUndoSnapshot()
		c.grid.edits.setUpdate(row, c.grid.cols[col], value)
		return ""
	}
	i := row - len(c.grid.rows)
	if i < 0 || i >= len(c.grid.edits.inserts) {
		return "该新行已不在队列里"
	}
	c.pushUndoSnapshot()
	c.grid.edits.setInsertEdit(i, c.grid.cols[col], value)
	c.afterEditQueued()
	return ""
}

// sortedInts returns map keys in ascending order.
func sortedInts(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// sortedUpdateRows returns update row indices in ascending order.
func sortedUpdateRows(m map[int]map[string]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// collectChanges turns the queue into RowChanges in queue order: updates by
// row, deletes by row, then the new rows. Column order follows the result, so
// the rendered statements stay stable.
func (c *Client) collectChanges() []db.RowChange {
	g := c.grid.edits
	if g == nil {
		return nil
	}
	var out []db.RowChange
	for _, i := range sortedUpdateRows(g.updates) {
		if g.deletes[i] {
			continue // the delete wins; editing a deleted row queues nothing new
		}
		out = append(out, db.RowChange{Original: c.grid.lastRows[i], Edits: c.editsOf(g.updates[i])})
	}
	for _, i := range sortedInts(g.deletes) {
		out = append(out, db.RowChange{Kind: db.RowDelete, Original: c.grid.lastRows[i]})
	}
	for _, row := range g.inserts {
		out = append(out, db.RowChange{Kind: db.RowInsert, Edits: c.editsOf(row)})
	}
	return out
}

// editsOf renders one cell map as CellEdits in result-column order. A queued
// value that is "" means NULL unless it was queued as a value - see
// gridEditState.nulls.
func (c *Client) editsOf(cells map[string]string) []db.CellEdit {
	edits := make([]db.CellEdit, 0, len(cells))
	for _, col := range c.grid.cols {
		v, ok := cells[col]
		if !ok {
			continue
		}
		if v == "" {
			edits = append(edits, db.CellEdit{Column: col, Value: nil})
		} else {
			edits = append(edits, db.CellEdit{Column: col, Value: v})
		}
	}
	return edits
}

// ---- grid rendering with edit marks ----

// Cell states driving the grid styling.
const (
	cellNormal = iota
	cellEdited
	cellDeleted
	cellInserted
)

// gridCellState renders the effective text and state of one cell, layering the
// edit queue on top of the base result.
func (c *Client) gridCellState(row, col int) (string, int) {
	colName := ""
	if col >= 0 && col < len(c.grid.cols) {
		colName = c.grid.cols[col]
	}
	if row >= 0 && row < len(c.grid.rows) {
		text := ""
		if col >= 0 && col < len(c.grid.rows[row]) {
			text = c.grid.rows[row][col]
		}
		if v, edited := c.grid.edits.updateAt(row, colName); edited {
			if v == "" {
				v = "NULL"
			}
			return v, cellEdited
		}
		if c.grid.edits.isDeleted(row) {
			return text, cellDeleted
		}
		return text, cellNormal
	}
	// A queued new row.
	i := row - len(c.grid.rows)
	if c.grid.edits == nil || i < 0 || i >= len(c.grid.edits.inserts) {
		return "", cellNormal
	}
	if v, ok := c.grid.edits.inserts[i][colName]; ok {
		if v == "" {
			v = "NULL"
		}
		return v, cellInserted
	}
	if col == 0 {
		return "(新行)", cellInserted
	}
	return "", cellInserted
}

// ---- edit context lifecycle ----

// setEditContext records the raw result and whether its rows are editable
// (SELECT * of one table), resetting any unsaved queue. Called after every
// executed statement.
func (c *Client) setEditContext(query string, res db.QueryResult) {
	c.grid.edits = newGridEditState()
	c.clearGridUndo()
	c.grid.lastRows = nil
	c.grid.src = db.TableRef{}
	if res.Error == "" {
		c.grid.lastRows = res.Rows
		c.grid.src = c.store.QuerySource(c.currentID, query, c.resolveDB())
	}
	c.updateEditButtons()
	c.refreshCrumb()
}

// gridEditable reports whether the current result can be written back.
func (c *Client) gridEditable() bool {
	return c.grid.src.Table != "" && c.grid.cols != nil
}

// editableTableName renders the object the current result reads from, which is
// where a filtered re-query has to point.
func (c *Client) editableTableName() (string, error) {
	conn, err := c.store.OpenConn(c.currentID)
	if err != nil {
		return "", err
	}
	typ := conn.Config.Type
	return db.TableName(typ, c.grid.src.Database, db.DefaultSchema(typ, c.grid.src.Schema), c.grid.src.Table)
}

// ---- actions ----

// multilineValue says whether a cell's value needs the taller box: a value with
// line breaks, or one too long to read across a single line. CJK counts wide,
// like the grid's own column measuring does.
func multilineValue(s string) bool {
	if strings.ContainsAny(s, "\n\r") {
		return true
	}
	n := 0
	for _, r := range s {
		if utf8.RuneLen(r) >= 3 {
			n += 2
		} else {
			n++
		}
	}
	return n > 80
}

// editSelectedCell opens a value dialog for the selected cell and queues the
// change. An empty input (or the NULL tick) writes NULL.
func (c *Client) editSelectedCell() {
	if !c.grid.hasCell || !c.gridEditable() {
		return
	}
	id := c.grid.cell
	if id.Col < 0 || id.Col >= len(c.grid.cols) {
		return
	}
	column := c.grid.cols[id.Col]
	if c.grid.edits.isDeleted(id.Row) {
		c.setStatusWarn("该行已标记删除")
		return
	}

	current := ""
	if id.Row < len(c.grid.lastRows) && id.Col < len(c.grid.lastRows[id.Row]) {
		if v := c.grid.lastRows[id.Row][id.Col]; v != nil {
			current = fmt.Sprintf("%v", v)
		}
	}

	// Two boxes, one of them in use. A JSON blob or a TEXT column that already
	// holds line breaks is unreadable - and unfixable - in a one-line entry, so
	// those get the taller box; everything else keeps Enter's meaning inside a
	// form, which is to submit it.
	single := widget.NewEntry()
	single.TextStyle = fyne.TextStyle{Monospace: true}
	single.SetText(current)
	multi := widget.NewMultiLineEntry()
	multi.TextStyle = fyne.TextStyle{Monospace: true}
	multi.SetText(current)
	multi.Wrapping = fyne.TextWrapOff
	useMulti := multilineValue(current)
	valueEntry := single
	if useMulti {
		valueEntry = multi
	}

	nullCheck := widget.NewCheck("写入 NULL（勾选后忽略输入）", nil)
	// The picker row is not here yet: its candidates come from another table,
	// so they are read after the dialog is up (see below).
	formItems := []*widget.FormItem{
		widget.NewFormItem("值", valueEntry),
		widget.NewFormItem("", nullCheck),
	}

	var d *dialog.CustomDialog
	save := func() {
		value := valueEntry.Text
		if nullCheck.Checked {
			value = ""
		}
		if err := c.setCellValue(id.Row, id.Col, value); err != "" {
			c.setStatusWarn(err)
			return
		}
		c.setStatus(column + " 已排队待保存")
		if d != nil {
			d.Hide()
		}
	}
	form := widget.NewForm(formItems...)
	form.OnSubmit = save
	form.SubmitText = "应用"

	d = dialog.NewCustom(fmt.Sprintf("编辑 %s", column), "取消", form, c.win)
	if useMulti {
		d.Resize(fyne.NewSize(480, 320))
	} else {
		d.Resize(fyne.NewSize(380, 220))
	}
	d.Show()

	// A single-column foreign key gets a picker fed by the referenced table,
	// so "7" and "12" can be told apart without opening it. That list is a SELECT
	// on another table, so the dialog opens without waiting for it and the row is
	// appended when the server answers - a remote box used to freeze the window
	// before the user could type anything. Closing the dialog gives up on the
	// read; no candidates or an error means no picker, which is what the entry
	// was always there for.
	src := c.grid.src
	connID := c.currentID
	var (
		cands db.ForeignKeyCandidates
		fkErr error
	)
	giveUp := c.background(func(ctx context.Context) {
		cands, fkErr = c.foreignKeyValues(ctx, connID, src.Database, src.Schema, src.Table, column, 0)
	}, func() {
		if fkErr != nil || len(cands.Values) == 0 {
			return
		}
		keyOf := make(map[string]string, len(cands.Values))
		options := make([]string, 0, len(cands.Values))
		for _, v := range cands.Values {
			opt := v.Value
			if v.Label != "" && v.Label != v.Value {
				opt = v.Value + " · " + v.Label
			}
			keyOf[opt] = v.Value
			options = append(options, opt)
		}
		fkSelect := widget.NewSelect(options, func(chosen string) {
			valueEntry.SetText(keyOf[chosen])
		})
		fkSelect.PlaceHolder = "从 " + cands.RefTable + " 选择"
		form.AppendItem(widget.NewFormItem("", fkSelect))
		if cands.Truncated {
			// The picker list is a capped SELECT; say when it is, or the
			// user assumes the box holds every reference value.
			note := widget.NewLabel("候选已截断，未列全——可直接输入需要的值")
			note.Importance = widget.LowImportance
			form.AppendItem(widget.NewFormItem("", note))
		}
	})
	d.SetOnClosed(giveUp)
}

// afterEditQueued refreshes the grid, counters and buttons after a change.
func (c *Client) afterEditQueued() {
	// Displayed values just changed, so an active search re-collects its hits.
	c.rescanGridSearch()
	// 有未落库的改动时这一行是提醒（警告色），空闲的「就绪」仍是普通汇报。
	if c.grid.edits.any() {
		c.setStatusWarn(c.editStatusText())
	} else {
		c.setStatus(c.editStatusText())
	}
	c.updateEditButtons()
}

// editStatusText summarises the on-screen result and its unsaved queue for the
// status bar. TablePro keeps a row count (and the pending-change count) in the
// grid's status line, so a loaded result reports "共 N 行" even with nothing
// pending; edits append the breakdown.
func (c *Client) editStatusText() string {
	g := c.grid.edits
	prefix := ""
	if n := len(c.grid.rows); n > 0 {
		prefix = fmt.Sprintf("共 %d 行", n)
	}
	if !g.any() {
		if prefix != "" {
			return prefix
		}
		return "就绪"
	}
	parts := make([]string, 0, 3)
	if n := len(g.updates); n > 0 {
		parts = append(parts, fmt.Sprintf("%d 处更新", n))
	}
	if n := len(g.inserts); n > 0 {
		parts = append(parts, fmt.Sprintf("%d 行新增", n))
	}
	if n := len(g.deletes); n > 0 {
		parts = append(parts, fmt.Sprintf("%d 行删除", n))
	}
	msg := "未保存：" + strings.Join(parts, "，")
	if prefix != "" {
		msg = prefix + " · " + msg
	}
	return msg
}

// insertRow queues an empty new row.
func (c *Client) insertRow() {
	if !c.gridEditable() {
		return
	}
	c.pushUndoSnapshot()
	c.grid.edits.inserts = append(c.grid.edits.inserts, make(map[string]string))
	c.applyGridRowHeight()
	c.afterEditQueued()
}

// deleteSelectedRow marks the selected row (or removes a queued new row).
func (c *Client) deleteSelectedRow() {
	if !c.grid.hasCell || !c.gridEditable() {
		return
	}
	row := c.grid.cell.Row
	if row < len(c.grid.rows) {
		c.pushUndoSnapshot()
		if c.grid.edits.deletes[row] {
			delete(c.grid.edits.deletes, row)
		} else {
			c.grid.edits.deletes[row] = true
			delete(c.grid.edits.updates, row)
		}
	} else {
		i := row - len(c.grid.rows)
		if i < 0 || i >= len(c.grid.edits.inserts) {
			return
		}
		c.pushUndoSnapshot()
		c.grid.edits.inserts = append(c.grid.edits.inserts[:i], c.grid.edits.inserts[i+1:]...)
		if c.grid.cell.Row >= len(c.grid.rows)+len(c.grid.edits.inserts) {
			c.grid.hasCell = false
		}
	}
	c.afterEditQueued()
}

// discardChanges drops the whole unsaved queue. The drop is undoable - Ctrl+Z
// brings the queue back - so a snapshot is pushed first.
func (c *Client) discardChanges() {
	if c.grid.edits.any() {
		c.pushUndoSnapshot()
	}
	c.grid.edits = newGridEditState()
	c.afterEditQueued()
}

// saveChanges previews the rendered statements and, on confirmation, applies
// them in one transaction, then re-runs the query to show the stored state.
// Both halves talk to the server - the preview reads the table's keys and types
// out of the catalog, the apply is the transaction itself - so neither runs on
// the event thread: on a remote box the old synchronous version froze the window
// before the preview even appeared. Closing the dialog gives up on whichever
// round trip is out.
func (c *Client) saveChanges() {
	set := db.ChangeSet{
		ConnID:   c.currentID,
		Database: c.resolveDB(),
		Schema:   c.grid.src.Schema,
		Table:    c.grid.src.Table,
		Columns:  c.grid.cols,
		Changes:  c.collectChanges(),
	}
	c.setStatus("生成语句 ...")
	var (
		statements []string
		buildErr   error
	)
	c.background(func(ctx context.Context) {
		statements, buildErr = c.store.BuildChanges(ctx, set)
	}, func() { c.showChangePreview(set, statements, buildErr) })
}

// showChangePreview puts the rendered statements in front of the user with the
// one button that can write them. UI thread only.
func (c *Client) showChangePreview(set db.ChangeSet, statements []string, err error) {
	if err != nil {
		dialog.ShowError(err, c.win)
		c.setStatus("就绪")
		return
	}

	preview := widget.NewMultiLineEntry()
	preview.TextStyle = fyne.TextStyle{Monospace: true}
	preview.Wrapping = fyne.TextWrapOff
	preview.Disable()
	preview.SetText(strings.Join(statements, "\n\n") + ";")

	var (
		d      *dialog.CustomDialog
		giveUp func()
		apply  func()
	)
	// Applying is a transaction, not a keystroke: a second round on the same
	// queue would run the statements twice. The button is the only way in, so it
	// goes away for the duration of the round trip.
	applying := false
	applyBtn := widget.NewButtonWithIcon("执行", theme.ConfirmIcon(), func() { apply() })
	applyBtn.Importance = widget.HighImportance
	apply = func() {
		if applying {
			return
		}
		applying = true
		applyBtn.Disable()
		c.setStatus("提交中 ...")
		var (
			applied int64
			appErr  error
		)
		giveUp = c.background(func(ctx context.Context) {
			applied, appErr = c.store.ApplyChanges(ctx, set)
		}, func() {
			applying = false
			if appErr != nil {
				applyBtn.Enable()
				c.setStatus("就绪")
				dialog.ShowError(appErr, c.win)
				return
			}
			c.setStatus(fmt.Sprintf("已提交 %d 行更改，正在刷新 ...", applied))
			d.Hide()
			c.runAllStatements() // re-reads the table and resets the queue
		})
	}
	buttons := container.NewHBox(applyBtn)

	content := container.NewBorder(nil, buttons, nil, nil, container.NewScroll(preview))
	d = dialog.NewCustom("将执行以下语句", "取消", content, c.win)
	d.SetOnClosed(func() {
		if giveUp != nil {
			giveUp()
		}
	})
	d.Resize(fyne.NewSize(640, 420))
	d.Show()
}

// updateEditButtons enables the edit actions to match the state.
func (c *Client) updateEditButtons() {
	if c.act.editCell == nil {
		return
	}
	editable := c.gridEditable()
	rowSelected := c.grid.hasCell
	rowIsDeleteable := editable && rowSelected

	set := func(b *widget.Button, on bool) {
		if on {
			b.Enable()
		} else {
			b.Disable()
		}
	}
	set(c.act.editCell, editable && rowSelected && !c.grid.edits.isDeleted(c.grid.cell.Row))
	set(c.act.insertRow, editable)
	set(c.act.deleteRow, rowIsDeleteable)
	set(c.act.saveChanges, c.grid.edits.any())
	set(c.act.discard, c.grid.edits.any())
	if c.act.filter != nil {
		set(c.act.filter, editable)
	}

	// The TablePlus-style bar over the grid: add / duplicate / delete mirror
	// the bottom bar's row actions (same handlers), reload stays live whenever
	// a result is on screen - a snapshot can always be re-fetched.
	if c.dbar.insertRow != nil {
		set(c.dbar.insertRow, editable)
		set(c.dbar.copyRow, editable && rowSelected)
		set(c.dbar.deleteRow, rowIsDeleteable)
		set(c.dbar.reload, c.grid.lastRows != nil)
	}
}
