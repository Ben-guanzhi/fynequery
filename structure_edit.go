package main

// Structure editing (TablePlus "Structure" slice, MySQL-first): a dialog to
// change a column's type/nullability, drop columns, and add new ones. The
// generated ALTER statements do NOT run immediately — they queue in
// ws.pendingDDL and ride the existing Preview / Commit / Rollback strip, so
// every DDL change is reviewable before it touches the server.

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"fynequery/proto"
)

// colEdit is one row of the structure dialog: an existing column's fields or
// a brand-new column being added.
type colEdit struct {
	name      string
	oldType   string
	nullable  bool
	isNew     bool
	nameEntry *widget.Entry
	typeEntry *widget.Entry
	nullChk   *widget.Check
	dropChk   *widget.Check
}

// editStructure opens the DDL-editing dialog for a table. The column list is
// fetched in the background first.
func (ws *workspace) editStructure(name string) {
	if ws.sess == nil {
		return
	}
	sess := ws.sess
	gen := ws.nextGen()
	safeGo("editStructure "+name, func() {
		cols, err := sess.Columns(name)
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, "Edit structure "+name, "Could not read the columns:\n"+err.Error())
				return
			}
			ws.showStructureDialog(name, cols)
		})
	})
}

// showStructureDialog builds the dialog and, on Apply, queues the ALTER
// statements into ws.pendingDDL (visible in the transaction strip).
func (ws *workspace) showStructureDialog(name string, cols []proto.ColumnInfo) {
	driver := ws.conn.Driver
	quote := func(s string) string { return quoteCol(s, driver) }

	rows := container.NewVBox()
	var edits []*colEdit
	for _, c := range cols {
		e := &colEdit{name: c.Name, oldType: c.Type, nullable: c.Nullable}
		e.typeEntry = widget.NewEntry()
		e.typeEntry.SetText(c.Type)
		e.nullChk = widget.NewCheck("NULL", nil)
		e.nullChk.SetChecked(c.Nullable)
		e.dropChk = widget.NewCheck("drop", nil)
		rows.Objects = append(rows.Objects, container.NewHBox(
			fixedLabel(c.Name), e.typeEntry, spacerW(6), e.nullChk, spacerW(6), e.dropChk))
		edits = append(edits, e)
	}

	// New-column row.
	newName := widget.NewEntry()
	newName.SetPlaceHolder("new column")
	newType := widget.NewEntry()
	newType.SetPlaceHolder("e.g. VARCHAR(64)")
	newNull := widget.NewCheck("NULL", nil)
	newNull.SetChecked(true)
	newEdit := &colEdit{isNew: true, nameEntry: newName, typeEntry: newType, nullChk: newNull}
	rows.Objects = append(rows.Objects, widget.NewSeparator(),
		container.NewHBox(fixedLabel("new col"), newName, newType, spacerW(6), newNull))

	var dlg dialog.Dialog
	apply := widget.NewButton("Queue DDL", func() {
		stmts := buildAlterDDL(ws.conn.Driver, name, quote, edits, newEdit)
		if len(stmts) == 0 {
			showInfo(ws.window, "Edit structure "+name, "No structural changes detected.")
			return
		}
		ws.pendingDDL = append(ws.pendingDDL, stmts...)
		dlg.Hide()
		ws.refreshTxnBar()
		showInfo(ws.window, "Edit structure "+name,
			fmt.Sprintf("%d ALTER statement(s) queued. Review via Preview, apply via Commit.", len(stmts)))
	})
	apply.Importance = widget.HighImportance
	cancel := widget.NewButton("Cancel", func() { dlg.Hide() })

	body := container.NewBorder(
		widget.NewLabel("Change a type or check drop, or add a new column below. Nothing runs until Commit."),
		container.NewHBox(layout.NewSpacer(), cancel, apply),
		nil, nil,
		container.NewScroll(container.NewPadded(rows)),
	)
	dlg = dialog.NewCustom("Edit structure — "+name, "", body, ws.window)
	dlg.Resize(fyne.NewSize(560, 440))
	dlg.Show()
}

// buildAlterDDL turns the dialog's edits into ALTER statements per dialect.
// Returns nil when nothing changed.
func buildAlterDDL(driver, table string, quote func(string) string, edits []*colEdit, newEdit *colEdit) []string {
	var out []string
	for _, e := range edits {
		if e.isNew {
			continue
		}
		if e.dropChk != nil && e.dropChk.Checked {
			out = append(out, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", quote(table), quote(e.name)))
			continue
		}
		newType := strings.TrimSpace(e.typeEntry.Text)
		typeChanged := newType != "" && !strings.EqualFold(newType, e.oldType)
		if typeChanged || e.nullChk.Checked != e.nullable {
			out = append(out, alterColumnType(driver, table, e.name, newType, e.nullChk.Checked, quote))
		}
	}
	if s := strings.TrimSpace(newEdit.nameEntry.Text); s != "" {
		typ := strings.TrimSpace(newEdit.typeEntry.Text)
		if typ == "" {
			typ = "VARCHAR(255)"
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", quote(table), quote(s), typ)
		if !newEdit.nullChk.Checked {
			stmt += " NOT NULL"
		}
		out = append(out, stmt)
	}
	return out
}

// alterColumnType renders the "change a column's type/nullability" statement
// for the driver. quote controls identifier quoting (kept consistent with
// the DROP/ADD statements in the same batch).
func alterColumnType(driver, table, col, newType string, nullable bool, quote func(string) string) string {
	q := quote
	// MySQL/MariaDB use MODIFY (type + nullability in one statement).
	if driver == "mysql" || driver == "mariadb" {
		stmt := "ALTER TABLE " + q(table) + " MODIFY COLUMN " + q(col)
		if newType != "" {
			stmt += " " + newType
		}
		if !nullable {
			stmt += " NOT NULL"
		}
		return stmt
	}
	// PostgreSQL-style: separate ALTER COLUMN statements.
	if driver == "postgres" || driver == "postgresql" {
		stmt := "ALTER TABLE " + q(table)
		if newType != "" {
			stmt += " ALTER COLUMN " + q(col) + " TYPE " + newType
			if nullable {
				stmt += ";\nALTER TABLE " + q(table) + " ALTER COLUMN " + q(col) + " DROP NOT NULL"
			} else {
				stmt += ";\nALTER TABLE " + q(table) + " ALTER COLUMN " + q(col) + " SET NOT NULL"
			}
			return stmt
		}
		if nullable {
			stmt += " ALTER COLUMN " + q(col) + " DROP NOT NULL"
		} else {
			stmt += " ALTER COLUMN " + q(col) + " SET NOT NULL"
		}
		return stmt
	}
	// Fallback (SQL Server / others): a simple ALTER COLUMN.
	stmt := "ALTER TABLE " + q(table) + " ALTER COLUMN " + q(col)
	if newType != "" {
		stmt += " " + newType
	}
	if !nullable {
		stmt += " NOT NULL"
	}
	return stmt
}

// fixedLabel renders a fixed-width label for the column-name cell.
func fixedLabel(text string) fyne.CanvasObject {
	l := widget.NewLabel(text)
	l.Truncation = fyne.TextTruncateEllipsis
	return container.NewGridWrap(fyne.NewSize(120, 0), l)
}
