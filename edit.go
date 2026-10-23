package main

// Editable data grid (TablePlus "edit mode"): double-click a cell to change
// its value, mark rows for deletion, then Commit (applies UPDATE/DELETE to the
// database) or Rollback. Only sessions implementing db.Updater (MySQL/MariaDB)
// can commit; the toolbar Edit button toggles the mode and the bottom bar
// shows a transaction strip while there are unsaved changes.

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// ---- dataTable editing state ----------------------------------------------

func (t *dataTable) hasPK() bool {
	for _, c := range t.data.Cols {
		if c.Key == "PK" {
			return true
		}
	}
	return false
}

func (t *dataTable) pkIndexes() []int {
	var out []int
	for i, c := range t.data.Cols {
		if c.Key == "PK" {
			out = append(out, i)
		}
	}
	return out
}

// setEditable turns editing on/off; it stays off for tables without a primary
// key (rows could not be identified).
func (t *dataTable) setEditable(e bool) {
	if e && !t.hasPK() {
		e = false
	}
	t.editable = e
	t.pkCols = nil
	if e {
		t.pkCols = t.pkIndexes()
	}
	t.Refresh()
}

func (t *dataTable) changeCount() int {
	n := len(t.deleted) + len(t.inserted)
	for _, m := range t.dirty {
		n += len(m)
	}
	return n
}

func (t *dataTable) clearDirty() {
	// Drop any staged (pending-insert) rows from the view first.
	if len(t.inserted) > 0 {
		var kept [][]*string
		for i, r := range t.data.Rows {
			if !t.inserted[i] {
				kept = append(kept, r)
			}
		}
		t.data.Rows = kept
		t.data.Fetched = len(t.data.Rows)
		if t.status != nil {
			t.status.setRows(t.data.Fetched, t.data.Total, false)
		}
	}
	t.dirty = nil
	t.deleted = nil
	t.inserted = nil
	t.Refresh()
	if t.onDirty != nil {
		t.onDirty()
	}
}

// stageInsert appends a pending new row (Add Row) instead of writing it to the
// database immediately. It rides the same Commit / Rollback / Preview strip as
// cell edits and deletions. Returns the new row index.
func (t *dataTable) stageInsert(values map[string]*string) int {
	row := make([]*string, len(t.data.Cols))
	for i, c := range t.data.Cols {
		if v, ok := values[c.Name]; ok {
			row[i] = v
		}
	}
	t.data.Rows = append(t.data.Rows, row)
	idx := len(t.data.Rows) - 1
	if t.inserted == nil {
		t.inserted = map[int]bool{}
	}
	t.inserted[idx] = true
	t.data.Fetched = len(t.data.Rows)
	if t.data.Fetched > t.data.Total {
		t.data.Total = t.data.Fetched
	}
	if t.status != nil {
		t.status.setRows(t.data.Fetched, t.data.Total, false)
	}
	t.Refresh()
	if t.rows != nil {
		t.rows.ensureVisible(idx)
	}
	if t.onDirty != nil {
		t.onDirty()
	}
	return idx
}

func (t *dataTable) setCell(row, col int, v *string) {
	// Compare against the stored (original) value: setting a cell back to its
	// original value is not a change and must not create a phantom pending edit.
	var orig *string
	if row >= 0 && row < len(t.data.Rows) {
		orig = cellStr(t.data.Rows[row], col)
	}
	if ptrEqual(orig, v) {
		t.revertCell(row, col)
		return
	}
	if t.dirty == nil {
		t.dirty = map[int]map[int]*string{}
	}
	if t.dirty[row] == nil {
		t.dirty[row] = map[int]*string{}
	}
	t.dirty[row][col] = v
	t.Refresh()
	if t.onDirty != nil {
		t.onDirty()
	}
}

// ptrEqual reports whether two *string values are both nil or hold equal text
// (used to detect no-op cell edits).
func ptrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (t *dataTable) revertCell(row, col int) {
	if m := t.dirty[row]; m != nil {
		delete(m, col)
		if len(m) == 0 {
			delete(t.dirty, row)
		}
	}
	t.Refresh()
	if t.onDirty != nil {
		t.onDirty()
	}
}

// markSelectedDeleted flags the selected row for deletion.
func (t *dataTable) markSelectedDeleted() bool {
	if t.selRow < 0 || t.selRow >= len(t.data.Rows) {
		return false
	}
	if t.deleted == nil {
		t.deleted = map[int]bool{}
	}
	t.deleted[t.selRow] = true
	delete(t.dirty, t.selRow) // an edited-then-deleted row is simply deleted
	t.Refresh()
	if t.onDirty != nil {
		t.onDirty()
	}
	return true
}

// pkOf builds the primary-key column/value map that identifies a row.
func (t *dataTable) pkOf(row int) map[string]*string {
	if row >= len(t.data.Rows) {
		return nil
	}
	pk := map[string]*string{}
	for _, ci := range t.pkCols {
		if ci < len(t.data.Cols) && ci < len(t.data.Rows[row]) {
			pk[t.data.Cols[ci].Name] = t.data.Rows[row][ci]
		}
	}
	return pk
}

type rowUpdate struct {
	pk      map[string]*string
	changes map[string]*string
}

// pending snapshots the unsaved edits and deletions for commit (UI thread).
func (t *dataTable) pending() (updates []rowUpdate, deletes, inserts []map[string]*string) {
	for row, m := range t.dirty {
		if t.deleted[row] || len(m) == 0 || row >= len(t.data.Rows) {
			continue
		}
		pk := t.pkOf(row)
		if len(pk) == 0 {
			continue
		}
		changes := map[string]*string{}
		for col, v := range m {
			if col < len(t.data.Cols) {
				changes[t.data.Cols[col].Name] = v
			}
		}
		updates = append(updates, rowUpdate{pk: pk, changes: changes})
	}
	for row := range t.deleted {
		if row >= len(t.data.Rows) {
			continue
		}
		if pk := t.pkOf(row); len(pk) > 0 {
			deletes = append(deletes, pk)
		}
	}
	for row := range t.inserted {
		if row >= len(t.data.Rows) {
			continue
		}
		vals := t.rowValues(row) // effective values (incl. edits to the staged row)
		ins := map[string]*string{}
		for i, c := range t.data.Cols {
			if i < len(vals) && vals[i] != nil {
				ins[c.Name] = vals[i] // nil (unset) columns are omitted -> DB default
			}
		}
		inserts = append(inserts, ins)
	}
	return updates, deletes, inserts
}

// beginEdit opens the cell editor for one cell.
func (t *dataTable) beginEdit(row, col int) {
	if t.window == nil || col >= len(t.data.Cols) {
		return
	}
	colName := t.data.Cols[col].Name
	var cur string
	isNull := true
	if vals := t.rowValues(row); col < len(vals) && vals[col] != nil {
		cur = *vals[col]
		isNull = false
	}

	entry := widget.NewMultiLineEntry()
	entry.SetText(cur)
	nullChk := widget.NewCheck("Set NULL", func(on bool) {
		if on {
			entry.SetText("")
			entry.Disable()
		} else {
			entry.Enable()
		}
	})
	nullChk.SetChecked(isNull)
	if isNull {
		entry.Disable()
	}

	var dlg dialog.Dialog
	setBtn := widget.NewButton("Set", func() {
		dlg.Hide()
		if nullChk.Checked {
			t.setCell(row, col, nil)
		} else {
			v := entry.Text
			t.setCell(row, col, &v)
		}
	})
	setBtn.Importance = widget.HighImportance
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })
	revertBtn := widget.NewButton("Revert cell", func() {
		dlg.Hide()
		t.revertCell(row, col)
	})
	// Revert only means something when this cell already carries a pending edit;
	// otherwise it would silently no-op, so keep it disabled.
	if _, dirty := t.dirty[row][col]; !dirty {
		revertBtn.Disable()
	}

	title := "Edit " + colName
	if id := t.rowID(row); id != "" {
		title += " — " + id
	}
	body := container.NewPadded(container.NewBorder(
		container.NewPadded(nullChk),
		container.NewHBox(revertBtn, layout.NewSpacer(), cancelBtn, setBtn),
		nil, nil,
		entry,
	))
	dlg = dialog.NewCustom(title, "", body, t.window)
	dlg.Resize(fyne.NewSize(460, 300))
	dlg.Show()
}

// rowID renders a short "pk=value" description of a row for dialog titles.
func (t *dataTable) rowID(row int) string {
	if row >= len(t.data.Rows) || len(t.pkCols) == 0 {
		return ""
	}
	var parts []string
	for _, ci := range t.pkCols {
		if ci >= len(t.data.Cols) || ci >= len(t.data.Rows[row]) {
			continue
		}
		v := "NULL"
		if t.data.Rows[row][ci] != nil {
			v = *t.data.Rows[row][ci]
		}
		parts = append(parts, t.data.Cols[ci].Name+"="+v)
	}
	return strings.Join(parts, ", ")
}

// ---- workspace edit-mode wiring -------------------------------------------

// prepGrid attaches the workspace context to a freshly built data grid and
// re-applies the column-visibility and edit-mode state.
func (ws *workspace) prepGrid(g *dataTable) {
	if g == nil {
		return
	}
	g.window = ws.window
	g.tableName = ws.tableName
	g.onGoto = ws.gotoTable
	g.onDirty = func() {
		ws.refreshTxnBar()
		ws.refreshRowDetails()
	}
	g.onRowSelect = func(int) { ws.refreshRowDetails() }
	ws.applyHidden(g)
	if ws.editMode {
		g.setEditable(true)
	}
}

// gotoTable opens the target table filtered by column = value — the click
// action behind Query-Magics "__goto__" aliases and TablePlus' FK arrows.
func (ws *workspace) gotoTable(table, column, value string) {
	// Jump to the target table filtered by column = value. Each table keeps its
	// own filter per tab, so the goto filter is applied to that tab directly.
	filter := ""
	if column != "" {
		filter = ws.quoteTable(column) + " = '" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	ws.openTableFiltered(table, filter)
}

// toggleEditMode flips the edit mode (toolbar button).
func (ws *workspace) toggleEditMode() {
	if ws.tableName == "" || ws.grid == nil {
		showInfo(ws.window, "Edit", "Open a table first.")
		return
	}
	if !ws.grid.hasPK() {
		showInfo(ws.window, "Edit",
			"This table has no primary key, so rows cannot be identified for editing.")
		return
	}
	ws.editMode = !ws.editMode
	if ws.editBtn != nil {
		ws.editBtn.setSelected(ws.editMode)
	}
	ws.grid.setEditable(ws.editMode)
	ws.refreshTxnBar()
}

// refreshTxnBar shows/hides the bottom transaction strip from the grid's
// unsaved-change count plus any queued structure DDL.
func (ws *workspace) refreshTxnBar() {
	if ws.commitBtn == nil {
		return
	}
	n := len(ws.pendingDDL)
	if ws.grid != nil {
		n += ws.grid.changeCount()
	}
	if n == 0 {
		ws.dirtyLabel.Hide()
		ws.commitBtn.Hide()
		ws.rollbackBtn.Hide()
		if ws.previewBtn != nil {
			ws.previewBtn.Hide()
		}
	} else {
		if d := len(ws.pendingDDL); d > 0 {
			ws.dirtyLabel.SetText(fmt.Sprintf("● %d unsaved change(s) · %d DDL", n-d, d))
		} else {
			ws.dirtyLabel.SetText(fmt.Sprintf("● %d unsaved change(s)", n))
		}
		ws.dirtyLabel.Show()
		ws.commitBtn.Show()
		ws.rollbackBtn.Show()
		if ws.previewBtn != nil {
			ws.previewBtn.Show()
		}
	}
	if ws.txnRow != nil {
		ws.txnRow.Refresh()
	}
}

// ---- right sidebar row details ---------------------------------------------

// refreshRowDetails rebuilds the right sidebar from the grid's selected row,
// falling back to the open table's structure when no row is selected.
func (ws *workspace) refreshRowDetails() {
	if ws.details == nil {
		return
	}
	g := ws.grid
	if g == nil || g.data == nil || g.selRow < 0 || g.selRow >= len(g.data.Rows) {
		if len(ws.tableCols) > 0 {
			ws.details.setTable(ws.tableName, ws.tableCols)
		} else {
			ws.details.setRowPrompt()
		}
		return
	}
	vals := g.rowValues(g.selRow)
	fields := make([]detailsField, 0, len(g.data.Cols))
	for i, col := range g.data.Cols {
		var v *string
		if i < len(vals) {
			v = vals[i]
		}
		fields = append(fields, detailsField{Name: col.Name, Type: col.Type, Value: v, Col: i})
	}
	ws.details.setRow(fields)
}

// showQueryRow fills the right 详情 pane from a row of a SQL query result. The
// values are read-only: a query result has no primary key to write back through.
func (ws *workspace) showQueryRow(data *TableData, row int) {
	if ws.details == nil {
		return
	}
	if data == nil || row < 0 || row >= len(data.Rows) {
		ws.details.setRowPrompt()
		return
	}
	r := data.Rows[row]
	fields := make([]detailsField, 0, len(data.Cols))
	for i, col := range data.Cols {
		var v *string
		if i < len(r) {
			v = r[i]
		}
		fields = append(fields, detailsField{
			Name: col.Name, Type: col.Type, Value: v, Col: i, ReadOnly: true,
		})
	}
	ws.details.setRow(fields)
}

// editRowField opens the cell editor for one field in the selected row. When
// the table has no primary key (rows can't be identified for editing) it falls
// back to a read-only value viewer, so a JSON / long-text field can still be
// inspected instead of dead-ending on the "no primary key" notice.
func (ws *workspace) editRowField(col int) {
	g := ws.grid
	if g == nil || g.data == nil || g.selRow < 0 {
		return
	}
	if !ws.editMode && !g.hasPK() {
		ws.viewRowField(col)
		return
	}
	if !ws.ensureEditable() {
		return
	}
	g.beginEdit(g.selRow, col)
}

// viewRowField opens the full-value viewer for one field of the selected row
// (read-only), pretty-printing JSON. Used when a cell cannot be edited.
func (ws *workspace) viewRowField(col int) {
	g := ws.grid
	if g == nil || g.data == nil || g.selRow < 0 || col < 0 || col >= len(g.data.Cols) {
		return
	}
	raw := ""
	if vals := g.rowValues(g.selRow); col < len(vals) && vals[col] != nil {
		raw = *vals[col]
	}
	showFullValueDialog(ws.window, g.data.Cols[col].Name, raw)
}

// setRowField applies a quick value (e.g. NULL) to one field of the row.
func (ws *workspace) setRowField(col int, v *string) {
	g := ws.grid
	if g == nil || g.data == nil || g.selRow < 0 {
		return
	}
	if !ws.ensureEditable() {
		return
	}
	g.setCell(g.selRow, col, v)
}

// ensureEditable turns on edit mode (auto-enabling it when the table has a
// primary key). It reports whether editing is possible.
func (ws *workspace) ensureEditable() bool {
	g := ws.grid
	if g == nil {
		return false
	}
	if ws.editMode {
		return true
	}
	if !g.hasPK() {
		showInfo(ws.window, "Edit",
			"This table has no primary key, so rows cannot be identified for editing.")
		return false
	}
	ws.editMode = true
	if ws.editBtn != nil {
		ws.editBtn.setSelected(true)
	}
	g.setEditable(true)
	ws.refreshTxnBar()
	return true
}

// commitEdits applies the pending UPDATE/DELETE operations plus any queued
// structure DDL, then reloads the page.
func (ws *workspace) commitEdits() {
	g := ws.grid
	if g == nil || (g.changeCount() == 0 && len(ws.pendingDDL) == 0) {
		return
	}
	up, ok := db.AsUpdater(ws.sess)
	updates, deletes, inserts := g.pending()
	if !ok && len(updates)+len(deletes)+len(inserts) > 0 {
		showInfo(ws.window, "Commit", "This connection does not support editing.")
		return
	}
	table := ws.tableName
	ddl := append([]string(nil), ws.pendingDDL...) // snapshot; cleared below on success
	// Safe Mode gate: committing pending edits is a write, so prompt per
	// the connection's level before the batch reaches the server.
	desc := fmt.Sprintf("commit %d row change(s)", g.changeCount())
	if len(inserts) > 0 {
		desc += fmt.Sprintf(", %d new row(s)", len(inserts))
	}
	if len(ddl) > 0 {
		desc += fmt.Sprintf(" and %d DDL statement(s)", len(ddl))
	}
	gateSafeMode(ws.window, ws.conn, "COMMIT — "+desc, func() {
		ws.commitGated(up, ok, table, updates, deletes, inserts, ddl)
	})
}

// commitGated applies one already-gated commit batch and reloads the page.
func (ws *workspace) commitGated(up db.Updater, ok bool, table string, updates []rowUpdate, deletes, inserts []map[string]*string, ddl []string) {
	progress := dialog.NewProgressInfinite("Commit", "Saving changes…", ws.window)
	progress.Show()
	safeGo("commit", func() {
		var ferr error
		if batcher, batched := db.AsBatchUpdater(ws.sess); batched && ok {
			// Atomic path: all row edits + DDL run in one transaction, so a
			// mid-batch error rolls back everything instead of leaving a partial
			// commit.
			rowEdits := make([]db.RowEdit, 0, len(updates))
			for _, u := range updates {
				rowEdits = append(rowEdits, db.RowEdit{PK: u.pk, Changes: u.changes})
			}
			ferr = batcher.ApplyChanges(table, rowEdits, deletes, inserts, ddl)
		} else {
			// Fallback for sessions without transaction support: one statement at
			// a time (each autocommit).
			if ok {
				for _, u := range updates {
					if err := up.UpdateRow(table, u.pk, u.changes); err != nil {
						ferr = err
						break
					}
				}
			}
			if ferr == nil && ok {
				for _, pk := range deletes {
					if err := up.DeleteRow(table, pk); err != nil {
						ferr = err
						break
					}
				}
			}
			// Then the staged new rows (INSERTs).
			if ferr == nil && ok {
				for _, ins := range inserts {
					if err := up.InsertRow(table, ins); err != nil {
						ferr = err
						break
					}
				}
			}
			// Then the queued structural DDL (ALTER statements).
			if ferr == nil && len(ddl) > 0 {
				for _, stmt := range ddl {
					if _, err := ws.sess.Query(stmt, 1); err != nil {
						ferr = fmt.Errorf("DDL: %w", err)
						break
					}
				}
			}
		}
		safeDo(func() {
			progress.Hide()
			if ferr != nil {
				if len(ddl) > 0 {
					ferr = fmt.Errorf("%w（排队的 %d 条 DDL 已丢弃，请手动重新排队）", ferr, len(ddl))
				}
				ws.pendingDDL = nil // failed commit: drop the queue, re-queue manually
				dialog.NewError(ferr, ws.window).Show()
				return
			}
			ws.pendingDDL = nil
			ws.reloadTable()
		})
	})
}

// rollbackEdits discards all unsaved changes and any queued DDL.
func (ws *workspace) rollbackEdits() {
	pending := 0
	if ws.grid != nil {
		pending = ws.grid.changeCount()
	}
	if pending == 0 && len(ws.pendingDDL) == 0 {
		return // nothing to discard
	}
	dialog.NewConfirm("Rollback",
		fmt.Sprintf("Discard %d unsaved change(s) and %d queued DDL statement(s)?", pending, len(ws.pendingDDL)),
		func(ok bool) {
			if !ok {
				return
			}
			if ws.grid != nil {
				ws.grid.clearDirty()
			}
			ws.pendingDDL = nil
			ws.refreshTxnBar()
		}, ws.window).Show()
}

// markRowDeleted flags the selected row for deletion (bottom-bar "− Row").
func (ws *workspace) markRowDeleted() {
	if !ws.editMode || ws.grid == nil {
		showInfo(ws.window, "Delete Row", "Enable edit mode first.")
		return
	}
	if !ws.grid.markSelectedDeleted() {
		showInfo(ws.window, "Delete Row", "Select a row first.")
	}
	ws.refreshTxnBar()
}

// showInsertRowDialog builds a form of column entries and inserts a row with
// the provided (non-empty) values.
func (ws *workspace) showInsertRowDialog() {
	if !ws.editMode || ws.grid == nil {
		showInfo(ws.window, "Add Row", "Enable edit mode first.")
		return
	}
	if _, ok := db.AsUpdater(ws.sess); !ok {
		showInfo(ws.window, "Add Row", "This connection does not support editing.")
		return
	}
	cols := ws.columnNames()
	if len(cols) == 0 {
		showInfo(ws.window, "Add Row", "Column list is not available yet.")
		return
	}

	entries := make([]*widget.Entry, len(cols))
	form := container.NewVBox()
	for i, name := range cols {
		e := widget.NewEntry()
		entries[i] = e
		form.Objects = append(form.Objects, container.NewBorder(nil, nil,
			widget.NewLabel(name), nil, e))
	}

	table := ws.tableName
	var dlg dialog.Dialog
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })
	insertBtn := widget.NewButton("Add", func() {
		values := map[string]*string{}
		for i, e := range entries {
			if e.Text != "" {
				v := e.Text
				values[cols[i]] = &v
			}
		}
		if len(values) == 0 {
			showInfo(ws.window, "Add Row", "Fill in at least one column.")
			return
		}
		dlg.Hide()
		// Stage the row (pending insert) like any other edit; Commit writes it.
		idx := ws.grid.stageInsert(values)
		ws.grid.selectRow(idx)
		ws.refreshTxnBar()
	})
	insertBtn.Importance = widget.HighImportance

	body := container.NewBorder(
		widget.NewLabel("Empty columns use the database default. The row is staged until you Commit."),
		container.NewHBox(layout.NewSpacer(), cancelBtn, insertBtn),
		nil, nil,
		container.NewScroll(form),
	)
	dlg = dialog.NewCustom("Add Row — "+table, "", body, ws.window)
	dlg.Resize(fyne.NewSize(480, 460))
	dlg.Show()
}
