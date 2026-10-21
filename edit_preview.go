package main

// Commit preview (TablePlus "Preview SQL"): renders the grid's pending
// UPDATE/DELETE operations as the statements Commit will send, so the user
// can review them before they touch the database.

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

// sqlLiteral renders one pending cell value as a SQL literal: NULL for nil,
// the DEFAULT keyword for the db.DefaultMarker sentinel, otherwise a
// single-quoted literal with quotes doubled.
func sqlLiteral(v *string) string {
	if v == nil {
		return "NULL"
	}
	if *v == db.DefaultMarker {
		return "DEFAULT"
	}
	return "'" + strings.ReplaceAll(*v, "'", "''") + "'"
}

// buildPreviewSQL renders the pending edits of one grid as statements for the
// given table (empty table name → "table" placeholder, e.g. query results).
func buildPreviewSQL(table, driver string, updates []rowUpdate, deletes []map[string]*string) string {
	if table == "" {
		table = "table"
	}
	var b strings.Builder
	qt := quoteCol(table, driver)
	for _, u := range updates {
		var sets, where []string
		for c, v := range u.changes {
			sets = append(sets, quoteCol(c, driver)+" = "+sqlLiteral(v))
		}
		for c, v := range u.pk {
			where = append(where, quoteCol(c, driver)+" = "+sqlLiteral(v))
		}
		fmt.Fprintf(&b, "UPDATE %s SET %s WHERE %s;\n",
			qt, strings.Join(sets, ", "), strings.Join(where, " AND "))
	}
	for _, pk := range deletes {
		var where []string
		for c, v := range pk {
			where = append(where, quoteCol(c, driver)+" = "+sqlLiteral(v))
		}
		fmt.Fprintf(&b, "DELETE FROM %s WHERE %s;\n", qt, strings.Join(where, " AND "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// previewEdits shows the statements Commit will send, with Commit / Cancel
// buttons so the review doubles as a confirmation step.
func (ws *workspace) previewEdits() {
	g := ws.grid
	if g == nil || (g.changeCount() == 0 && len(ws.pendingDDL) == 0) {
		return
	}
	updates, deletes := g.pending()
	sqlText := buildPreviewSQL(ws.tableName, ws.conn.Driver, updates, deletes)
	if d := strings.Join(ws.pendingDDL, "\n"); d != "" {
		if sqlText != "" {
			sqlText += "\n\n"
		}
		sqlText += "-- pending structure changes\n" + d
	}
	if sqlText == "" {
		showInfo(ws.window, "Preview", "Nothing to commit.")
		return
	}

	code := widget.NewMultiLineEntry()
	code.TextStyle.Monospace = true
	code.SetText(sqlText)
	code.Wrapping = fyne.TextWrapOff

	commitBtn := widget.NewButton("Commit", func() {
		ws.commitEdits()
	})
	commitBtn.Importance = widget.HighImportance
	cancelBtn := widget.NewButton("Cancel", func() {})
	var d dialog.Dialog
	cancelBtn.OnTapped = func() { d.Hide() }
	commitBtn.OnTapped = func() {
		d.Hide()
		ws.commitEdits()
	}
	body := container.NewBorder(
		widget.NewLabel("Commit will send these statements:"),
		container.NewHBox(layout.NewSpacer(), cancelBtn, commitBtn),
		nil, nil,
		container.NewScroll(code),
	)
	d = dialog.NewCustom("Preview SQL — "+ws.tableName, "", body, ws.window)
	d.Resize(fyne.NewSize(560, 420))
	d.Show()
}
