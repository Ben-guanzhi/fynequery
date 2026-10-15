package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
	deletes map[int]bool
	inserts []map[string]string
}

func newGridEditState() *gridEditState {
	return &gridEditState{
		updates: make(map[int]map[string]string),
		deletes: make(map[int]bool),
	}
}

func (g *gridEditState) any() bool {
	return g != nil && (len(g.updates) > 0 || len(g.deletes) > 0 || len(g.inserts) > 0)
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

// editsOf renders one cell map as CellEdits in result-column order. An empty
// queued value means NULL.
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
	c.grid.lastRows = nil
	c.grid.src = db.TableRef{}
	if res.Error == "" {
		c.grid.lastRows = res.Rows
		c.grid.src = c.store.QuerySource(c.currentID, query, c.resolveDB())
	}
	c.updateEditButtons()
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
		c.setStatus("该行已标记删除")
		return
	}

	current := ""
	if id.Row < len(c.grid.lastRows) && id.Col < len(c.grid.lastRows[id.Row]) {
		if v := c.grid.lastRows[id.Row][id.Col]; v != nil {
			current = fmt.Sprintf("%v", v)
		}
	}

	valueEntry := widget.NewEntry()
	valueEntry.SetText(current)
	valueEntry.TextStyle = fyne.TextStyle{Monospace: true}
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
		if id.Row < len(c.grid.rows) {
			c.grid.edits.setUpdate(id.Row, column, value)
		} else {
			c.grid.edits.setInsertEdit(id.Row-len(c.grid.rows), column, value)
		}
		c.afterEditQueued()
		if d != nil {
			d.Hide()
		}
	}
	form := widget.NewForm(formItems...)
	form.OnSubmit = save
	form.SubmitText = "应用"

	d = dialog.NewCustom(fmt.Sprintf("编辑 %s", column), "取消", form, c.win)
	d.Resize(fyne.NewSize(380, 220))
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
	})
	d.SetOnClosed(giveUp)
}

// afterEditQueued refreshes the grid, counters and buttons after a change.
func (c *Client) afterEditQueued() {
	// Displayed values just changed, so an active search re-collects its hits.
	c.rescanGridSearch()
	c.setStatus(c.editStatusText())
	c.updateEditButtons()
}

// editStatusText summarises the unsaved queue for the status bar.
func (c *Client) editStatusText() string {
	g := c.grid.edits
	if !g.any() {
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
	return "未保存：" + strings.Join(parts, "，")
}

// insertRow queues an empty new row.
func (c *Client) insertRow() {
	if !c.gridEditable() {
		return
	}
	c.grid.edits.inserts = append(c.grid.edits.inserts, make(map[string]string))
	c.afterEditQueued()
}

// deleteSelectedRow marks the selected row (or removes a queued new row).
func (c *Client) deleteSelectedRow() {
	if !c.grid.hasCell || !c.gridEditable() {
		return
	}
	row := c.grid.cell.Row
	if row < len(c.grid.rows) {
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
		c.grid.edits.inserts = append(c.grid.edits.inserts[:i], c.grid.edits.inserts[i+1:]...)
		if c.grid.cell.Row >= len(c.grid.rows)+len(c.grid.edits.inserts) {
			c.grid.hasCell = false
		}
	}
	c.afterEditQueued()
}

// discardChanges drops the whole unsaved queue.
func (c *Client) discardChanges() {
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
			c.runCurrent() // re-reads the table and resets the queue
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
}
