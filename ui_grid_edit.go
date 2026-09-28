package main

import (
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
	g := c.edits
	if g == nil {
		return nil
	}
	var out []db.RowChange
	for _, i := range sortedUpdateRows(g.updates) {
		if g.deletes[i] {
			continue // the delete wins; editing a deleted row queues nothing new
		}
		out = append(out, db.RowChange{Original: c.lastRows[i], Edits: c.editsOf(g.updates[i])})
	}
	for _, i := range sortedInts(g.deletes) {
		out = append(out, db.RowChange{Kind: db.RowDelete, Original: c.lastRows[i]})
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
	for _, col := range c.gridCols {
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
	if col >= 0 && col < len(c.gridCols) {
		colName = c.gridCols[col]
	}
	if row >= 0 && row < len(c.gridRows) {
		text := ""
		if col >= 0 && col < len(c.gridRows[row]) {
			text = c.gridRows[row][col]
		}
		if v, edited := c.edits.updateAt(row, colName); edited {
			if v == "" {
				v = "NULL"
			}
			return v, cellEdited
		}
		if c.edits.isDeleted(row) {
			return text, cellDeleted
		}
		return text, cellNormal
	}
	// A queued new row.
	i := row - len(c.gridRows)
	if c.edits == nil || i < 0 || i >= len(c.edits.inserts) {
		return "", cellNormal
	}
	if v, ok := c.edits.inserts[i][colName]; ok {
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
	c.edits = newGridEditState()
	c.lastRows = nil
	c.sourceRef = db.TableRef{}
	if res.Error == "" {
		c.lastRows = res.Rows
		c.sourceRef = c.store.QuerySource(c.currentID, query, c.resolveDB())
	}
	c.updateEditButtons()
}

// gridEditable reports whether the current result can be written back.
func (c *Client) gridEditable() bool {
	return c.sourceRef.Table != "" && c.gridCols != nil
}

// editableTableName renders the object the current result reads from, which is
// where a filtered re-query has to point.
func (c *Client) editableTableName() (string, error) {
	conn, err := c.store.OpenConn(c.currentID)
	if err != nil {
		return "", err
	}
	typ := conn.Config.Type
	return db.TableName(typ, c.sourceRef.Database, db.DefaultSchema(typ, c.sourceRef.Schema), c.sourceRef.Table)
}

// ---- actions ----

// editSelectedCell opens a value dialog for the selected cell and queues the
// change. An empty input (or the NULL tick) writes NULL.
func (c *Client) editSelectedCell() {
	if !c.hasCell || !c.gridEditable() {
		return
	}
	id := c.selectedCell
	if id.Col < 0 || id.Col >= len(c.gridCols) {
		return
	}
	column := c.gridCols[id.Col]
	if c.edits.isDeleted(id.Row) {
		c.setStatus("该行已标记删除")
		return
	}

	current := ""
	if id.Row < len(c.lastRows) && id.Col < len(c.lastRows[id.Row]) {
		if v := c.lastRows[id.Row][id.Col]; v != nil {
			current = fmt.Sprintf("%v", v)
		}
	}

	valueEntry := widget.NewEntry()
	valueEntry.SetText(current)
	valueEntry.TextStyle = fyne.TextStyle{Monospace: true}
	nullCheck := widget.NewCheck("写入 NULL（勾选后忽略输入）", nil)

	// A single-column foreign key gets a picker fed by the referenced table,
	// so "7" and "12" can be told apart without opening it.
	formItems := []*widget.FormItem{
		widget.NewFormItem("值", valueEntry),
		widget.NewFormItem("", nullCheck),
	}
	if cands, err := c.foreignKeyValues(c.currentID, c.sourceRef.Database,
		c.sourceRef.Schema, c.sourceRef.Table, column, 0); err == nil && len(cands.Values) > 0 {
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
		formItems = append(formItems, widget.NewFormItem("", fkSelect))
	}

	var d *dialog.CustomDialog
	save := func() {
		value := valueEntry.Text
		if nullCheck.Checked {
			value = ""
		}
		if id.Row < len(c.gridRows) {
			c.edits.setUpdate(id.Row, column, value)
		} else {
			c.edits.setInsertEdit(id.Row-len(c.gridRows), column, value)
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
	g := c.edits
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
	c.edits.inserts = append(c.edits.inserts, make(map[string]string))
	c.afterEditQueued()
}

// deleteSelectedRow marks the selected row (or removes a queued new row).
func (c *Client) deleteSelectedRow() {
	if !c.hasCell || !c.gridEditable() {
		return
	}
	row := c.selectedCell.Row
	if row < len(c.gridRows) {
		if c.edits.deletes[row] {
			delete(c.edits.deletes, row)
		} else {
			c.edits.deletes[row] = true
			delete(c.edits.updates, row)
		}
	} else {
		i := row - len(c.gridRows)
		if i < 0 || i >= len(c.edits.inserts) {
			return
		}
		c.edits.inserts = append(c.edits.inserts[:i], c.edits.inserts[i+1:]...)
		if c.selectedCell.Row >= len(c.gridRows)+len(c.edits.inserts) {
			c.hasCell = false
		}
	}
	c.afterEditQueued()
}

// discardChanges drops the whole unsaved queue.
func (c *Client) discardChanges() {
	c.edits = newGridEditState()
	c.afterEditQueued()
}

// saveChanges previews the rendered statements and, on confirmation, applies
// them in one transaction, then re-runs the query to show the stored state.
func (c *Client) saveChanges() {
	set := db.ChangeSet{
		ConnID:   c.currentID,
		Database: c.resolveDB(),
		Schema:   c.sourceRef.Schema,
		Table:    c.sourceRef.Table,
		Columns:  c.gridCols,
		Changes:  c.collectChanges(),
	}
	statements, err := c.store.BuildChanges(set)
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}

	preview := widget.NewMultiLineEntry()
	preview.TextStyle = fyne.TextStyle{Monospace: true}
	preview.Wrapping = fyne.TextWrapOff
	preview.Disable()
	preview.SetText(strings.Join(statements, "\n\n") + ";")

	var d *dialog.CustomDialog
	apply := func() {
		applied, err := c.store.ApplyChanges(set)
		if err != nil {
			dialog.ShowError(err, c.win)
			return
		}
		c.setStatus(fmt.Sprintf("已提交 %d 行更改，正在刷新 ...", applied))
		d.Hide()
		c.runCurrent() // re-reads the table and resets the queue
	}
	applyBtn := widget.NewButtonWithIcon("执行", theme.ConfirmIcon(), apply)
	applyBtn.Importance = widget.HighImportance
	buttons := container.NewHBox(applyBtn)

	content := container.NewBorder(nil, buttons, nil, nil, container.NewScroll(preview))
	d = dialog.NewCustom("将执行以下语句", "取消", content, c.win)
	d.Resize(fyne.NewSize(640, 420))
	d.Show()
}

// updateEditButtons enables the edit actions to match the state.
func (c *Client) updateEditButtons() {
	if c.editCellBtn == nil {
		return
	}
	editable := c.gridEditable()
	rowSelected := c.hasCell
	rowIsDeleteable := editable && rowSelected

	set := func(b *widget.Button, on bool) {
		if on {
			b.Enable()
		} else {
			b.Disable()
		}
	}
	set(c.editCellBtn, editable && rowSelected && !c.edits.isDeleted(c.selectedCell.Row))
	set(c.insertRowBtn, editable)
	set(c.deleteRowBtn, rowIsDeleteable)
	set(c.saveChangesBtn, c.edits.any())
	set(c.discardBtn, c.edits.any())
	if c.filterBtn != nil {
		set(c.filterBtn, editable)
	}
}
