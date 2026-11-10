package main

// Commit preview (TablePlus "Preview SQL"): renders the grid's pending
// UPDATE/DELETE operations as the statements Commit will send, so the user
// can review them before they touch the database.

import (
	"fmt"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"

	"github.com/dbcore/dbcore/sqltext"
)

// sqlLiteral renders one pending cell value as a SQL literal: NULL for nil,
// the DEFAULT keyword for the db.DefaultMarker sentinel, otherwise a
// single-quoted literal. The escaping comes from the dialect (MySQL also
// escapes backslashes): a preview that showed `'\'` for a MySQL value the
// commit would send as `'\\'` would not be the SQL being run.
func sqlLiteral(driver string, v *string) string {
	if v == nil {
		return "NULL"
	}
	if *v == db.DefaultMarker {
		return "DEFAULT"
	}
	return sqltext.QuotedLiteral(driver, *v)
}

// buildPreviewSQL renders the pending edits of one grid as statements for the
// given table (empty table name → "table" placeholder, e.g. query results).
func buildPreviewSQL(table, driver string, updates []rowUpdate, deletes, inserts []map[string]*string) string {
	if table == "" {
		table = "table"
	}
	var b strings.Builder
	qt := quoteCol(table, driver)
	for _, u := range updates {
		var sets, where []string
		for c, v := range u.changes {
			sets = append(sets, quoteCol(c, driver)+" = "+sqlLiteral(driver, v))
		}
		for c, v := range u.pk {
			where = append(where, quoteCol(c, driver)+" = "+sqlLiteral(driver, v))
		}
		fmt.Fprintf(&b, "UPDATE %s SET %s WHERE %s;\n",
			qt, strings.Join(sets, ", "), strings.Join(where, " AND "))
	}
	for _, pk := range deletes {
		var where []string
		for c, v := range pk {
			where = append(where, quoteCol(c, driver)+" = "+sqlLiteral(driver, v))
		}
		fmt.Fprintf(&b, "DELETE FROM %s WHERE %s;\n", qt, strings.Join(where, " AND "))
	}
	for _, ins := range inserts {
		// The commit omits DEFAULT-sentinel columns so the server applies its
		// own default (buildInsert); the preview must show exactly that
		// statement, not a DEFAULT keyword the run would never send.
		cols := make([]string, 0, len(ins))
		for c, v := range ins {
			if v != nil && *v == db.DefaultMarker {
				continue
			}
			cols = append(cols, c)
		}
		if len(cols) == 0 {
			fmt.Fprintf(&b, "-- INSERT skipped: every column uses DEFAULT (commit will report “no values”)\n")
			continue
		}
		sort.Strings(cols) // stable column order for a readable preview
		colParts := make([]string, len(cols))
		valParts := make([]string, len(cols))
		for i, c := range cols {
			colParts[i] = quoteCol(c, driver)
			valParts[i] = sqlLiteral(driver, ins[c])
		}
		fmt.Fprintf(&b, "INSERT INTO %s (%s) VALUES (%s);\n",
			qt, strings.Join(colParts, ", "), strings.Join(valParts, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// previewEdits shows the statements Commit will send, with Commit / Cancel
// buttons so the review doubles as a confirmation step.
func (ws *workspace) previewEdits() {
	g := ws.grid
	tab, cU, cD, cI := ws.carriedEditsOf()
	carry := 0
	if tab != nil {
		carry = tab.carryCount()
	}
	if g == nil || (g.changeCount() == 0 && len(ws.pendingDDL) == 0 && carry == 0) {
		return
	}
	updates, deletes, inserts := g.pending()
	// The preview must show what commit will send — the carry-over included,
	// same merge as commitEdits (建议5: 所见 SQL 即所执行 SQL).
	updates = append(updates, cU...)
	deletes = append(deletes, cD...)
	inserts = append(inserts, cI...)
	sqlText := buildPreviewSQL(ws.tableName, ws.conn.Driver, updates, deletes, inserts)
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
