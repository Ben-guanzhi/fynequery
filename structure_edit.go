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

	"github.com/Ben-guanzhi/dbcore"
	"fynequery/db"
	dbcoreddl "github.com/Ben-guanzhi/dbcore/ddl"

	"fynequery/proto"
)

// colEdit is one row of the structure dialog: an existing column's fields or
// a brand-new column being added.
type colEdit struct {
	name      string
	// oldName is the column's name when the dialog opened; a non-empty
	// oldName that differs from nameEntry's text is a RENAME (the kernel's
	// RenameColumn keeps the data, unlike a drop+add which loses it).
	oldName   string
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
	progress := dialog.NewProgressInfinite("Edit structure", "Reading columns of "+name+"…", ws.window)
	progress.Show()
	safeGo("editStructure "+name, func() {
		cols, err := sess.Columns(name)
		safeDo(func() {
			progress.Hide()
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, "Edit structure "+name, "Could not read the columns:\n"+err.Error())
				return
			}
			idxs, ierr := listIndexesFor(sess, name)
			if ierr != nil {
				idxs = nil // 索引面读不到不阻塞列编辑（引擎不支持时为空）
			}
			ws.showStructureDialog(name, cols, idxs)
		})
	})
}

// showStructureDialog builds the dialog and, on Apply, queues the ALTER
// statements into ws.pendingDDL (visible in the transaction strip).
func (ws *workspace) showStructureDialog(name string, cols []proto.ColumnInfo, idxs []dbcore.IndexInfo) {
	driver := ws.conn.Driver
	quote := func(s string) string { return quoteCol(s, driver) }

	rows := container.NewVBox()
	var edits []*colEdit
	for _, c := range cols {
		e := &colEdit{name: c.Name, oldName: c.Name, oldType: c.Type, nullable: c.Nullable}
		e.nameEntry = widget.NewEntry()
		e.nameEntry.SetText(c.Name)
		e.typeEntry = widget.NewEntry()
		e.typeEntry.SetText(c.Type)
		e.nullChk = widget.NewCheck("NULL", nil)
		e.nullChk.SetChecked(c.Nullable)
		e.dropChk = widget.NewCheck("Drop", nil)
		rows.Objects = append(rows.Objects, container.NewHBox(
			e.nameEntry, e.typeEntry, spacerW(6), e.nullChk, spacerW(6), e.dropChk))
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
	rows.Objects = append(rows.Objects, spacerH(8),
		container.NewHBox(fixedLabel("new col"), newName, newType, spacerW(6), newNull))
	idxDrops, idxName, idxCols, idxUnique := showIndexSection(rows, idxs)

	var dlg dialog.Dialog
	apply := widget.NewButton("Queue DDL", func() {
		if nn := strings.TrimSpace(newName.Text); nn != "" {
			for _, c := range cols {
				if strings.EqualFold(c.Name, nn) {
					showInfo(ws.window, "Edit structure "+name, "Column "+nn+" already exists.")
					return
				}
			}
		}
		stmts := buildAlterDDL(ws.conn.Driver, name, quote, edits, newEdit)
		stmts = append(stmts, buildIndexDDL(ws.conn.Driver, name, idxDrops, idxName, idxCols, idxUnique)...)
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

	body := container.NewPadded(container.NewBorder(
		widget.NewLabel("Change a type or check drop, or add a new column below. Nothing runs until Commit."),
		container.NewHBox(layout.NewSpacer(), cancel, apply),
		nil, nil,
		container.NewScroll(container.NewPadded(rows)),
	))
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
		// A rename: the row opened with oldName and its name entry now says
		// something else. Route through the kernel's RenameColumn (dbcore
		// v0.14.0) — it keeps the data, refuses engines that cannot express
		// it (MySQL 5.x needs CHANGE with a full definition), and keeps the
		// dialect spelling in one place.
		// nameEntry may be nil on callers that build a colEdit without one
		// (tests, programmatic use) — only a dialog row can rename.
		var newName string
		if e.nameEntry != nil {
			newName = strings.TrimSpace(e.nameEntry.Text)
		}
		if e.oldName != "" && newName != "" && !strings.EqualFold(newName, e.oldName) {
			stmt, err := dbcoreddl.RenameColumn(driver, "", table, e.oldName, newName)
			if err != nil {
				// 名字冲突等失败由 Preview 暴露——与其它排队语句同路。
				out = append(out, "-- "+err.Error())
				continue
			}
			out = append(out, stmt)
			continue
		}
		newType := strings.TrimSpace(e.typeEntry.Text)
		typeChanged := newType != "" && !strings.EqualFold(newType, e.oldType)
		if typeChanged || e.nullChk.Checked != e.nullable {
			out = append(out, alterColumnType(driver, table, e.name, newType, e.oldType, e.nullChk.Checked, quote))
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
//
// oldType is the column's current type. A nullability-only edit leaves the
// type entry empty, and MySQL's MODIFY requires the full type — so the old
// type is reused there instead of emitting "MODIFY COLUMN col NOT NULL",
// which the server rejects.
func alterColumnType(driver, table, col, newType, oldType string, nullable bool, quote func(string) string) string {
	q := quote
	// MySQL/MariaDB use MODIFY (type + nullability in one statement).
	if driver == "mysql" || driver == "mariadb" {
		// MODIFY needs a complete type; a nullability-only edit names none.
		if newType == "" {
			newType = oldType
		}
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

// listIndexesFor 通过 IndexLister 取一张表的索引（引擎不支持返回空）。
func listIndexesFor(sess db.Session, table string) ([]dbcore.IndexInfo, error) {
	lister, ok := sess.(db.IndexLister)
	if !ok {
		return nil, nil
	}
	return lister.ListIndexes("", table)
}

// indexDropRow 是结构对话框里一个可勾选删除的现有索引。
type indexDropRow struct {
	info dbcore.IndexInfo
	chk  *widget.Check
}

// showIndexSection 把索引区（现有索引勾选 Drop + 新增行）画进 rows，
// 返回收集器供 Apply 时生成语句。
func showIndexSection(rows *fyne.Container, idxs []dbcore.IndexInfo) (drops []*indexDropRow, newName, newCols *widget.Entry, newUnique *widget.Check) {
	if len(idxs) == 0 {
		return nil, nil, nil, nil
	}
	rows.Objects = append(rows.Objects, spacerH(8), widget.NewLabel("Indexes"))
	for _, ix := range idxs {
		if ix.Primary {
			continue // 主键不是索引（AddPrimaryKey 的领域，删除走 DropPrimaryKey）
		}
		row := &indexDropRow{info: ix, chk: widget.NewCheck("", nil)}
		cols := strings.Join(ix.Columns, ", ")
		rows.Objects = append(rows.Objects, container.NewHBox(
			row.chk, widget.NewLabel(ix.Name), widget.NewLabel("("+cols+")")))
		drops = append(drops, row)
	}
	newName = widget.NewEntry()
	newName.SetPlaceHolder("new index")
	newCols = widget.NewEntry()
	newCols.SetPlaceHolder("columns, comma separated")
	newUnique = widget.NewCheck("UNIQUE", nil)
	rows.Objects = append(rows.Objects, container.NewHBox(
		widget.NewLabel("new"), newName, newCols, newUnique))
	return drops, newName, newCols, newUnique
}

// buildIndexDDL 把索引区的勾选/新增变成内核渲染的 CREATE/DROP INDEX 语句。
func buildIndexDDL(driver, table string, drops []*indexDropRow, newName, newCols *widget.Entry, newUnique *widget.Check) []string {
	var out []string
	for _, d := range drops {
		if d.chk != nil && d.chk.Checked {
			out = append(out, dbcoreddl.DropIndex(driver, "", table, d.info.Name))
		}
	}
	name := strings.TrimSpace(newName.Text)
	cols := strings.TrimSpace(newCols.Text)
	if name != "" && cols != "" {
		var collist []string
		for _, c := range strings.Split(cols, ",") {
			if cc := strings.TrimSpace(c); cc != "" {
				collist = append(collist, cc)
			}
		}
		if len(collist) > 0 {
			out = append(out, dbcoreddl.CreateIndex(driver, "", table, dbcoreddl.Index{
				Name: name, Unique: newUnique.Checked, Columns: collist,
			}))
		}
	}
	return out
}
