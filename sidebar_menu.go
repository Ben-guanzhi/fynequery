package main

// Left-sidebar item context menu (TablePlus "Item actions"): open, view
// structure, copy the object's name or a SQL script, and clone/truncate/drop.
// Triggered by a right-click on a tree item (see treeItem.TappedSecondary).

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"fynequery/proto"
)

// showItemMenu pops the context menu for the right-clicked sidebar item at the
// given absolute position.
func (ws *workspace) showItemMenu(name string, pos fyne.Position) {
	if ws.body == nil || name == "" {
		return
	}
	canvas := fyne.CurrentApp().Driver().CanvasForObject(ws.body)
	if canvas == nil {
		return
	}
	quote := ws.quoteTable(name)

	pinItem := fyne.NewMenuItem("置顶", func() { ws.side.togglePin(name) })
	pinItem.Disabled = name == ""
	if ws.side != nil && ws.side.isPinned(name) {
		pinItem = fyne.NewMenuItem("取消置顶", func() { ws.side.togglePin(name) })
	}

	// The picked object's kind decides whether "Open" shows data or DDL.
	kind := "table"
	for _, o := range ws.side.objects {
		if o.Name == name {
			kind = o.Kind
			break
		}
	}

	menu := fyne.NewMenu("",
		pinItem,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("打开", func() { ws.openObjectByName(name) }),
		fyne.NewMenuItem("查県 DDL", func() { ws.openObjectDDL(name, kind) }),
		fyne.NewMenuItem("编辑结构…", func() { ws.editStructure(name) }),
		fyne.NewMenuItem("查県结构", func() {
			ws.openTable(name)
			if ws.segment != nil {
				ws.segment.set(1)
			}
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("复制名称", func() { ws.copyText(name) }),
		fyne.NewMenuItem("复制为 CREATE", func() { ws.copyCreateScript(name) }),
		fyne.NewMenuItem("复制为 SELECT", func() {
			ws.copyText("SELECT * FROM " + quote + " LIMIT 100;")
		}),
		fyne.NewMenuItem("复制为 TRUNCATE", func() { ws.copyText(ws.truncateSQL(name, quote)) }),
		fyne.NewMenuItem("复制为 DROP", func() { ws.copyText("DROP TABLE " + quote + ";") }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("克隆...", func() { ws.cloneTable(name, quote) }),
		fyne.NewMenuItem("清空...", func() { ws.confirmTruncate(name, quote) }),
		fyne.NewMenuItem("删除...", func() { ws.confirmDrop(name, quote) }),
	)
	widget.ShowPopUpMenuAtPosition(menu, canvas, pos)
}

// copyText puts s on the system clipboard.
func (ws *workspace) copyText(s string) {
	fyne.CurrentApp().Clipboard().SetContent(s)
}

// copyCreateScript fetches the table's columns and copies a CREATE TABLE
// script (best effort — the dialect types are used verbatim).
func (ws *workspace) copyCreateScript(name string) {
	if ws.sess == nil {
		return
	}
	driver := ws.conn.Driver
	quote := ws.quoteTable(name)
	safeGo("copyCreate "+name, func() {
		cols, err := ws.sess.Columns(name)
		if err != nil {
			safeDo(func() { showInfo(ws.window, "Copy as CREATE", err.Error()) })
			return
		}
		script := buildCreateScript(name, quote, cols, driver)
		safeDo(func() {
			ws.copyText(script)
			showInfo(ws.window, "Copy as CREATE", "Script copied to clipboard.")
		})
	})
}

// buildCreateScript renders a CREATE TABLE statement from column metadata.
// It is intentionally simple: column types are copied as reported and the
// primary-key columns become a table-level PRIMARY KEY constraint.
func buildCreateScript(name, quote string, cols []proto.ColumnInfo, driver string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", quote)
	var pk []string
	for i, c := range cols {
		fmt.Fprintf(&b, "  %s %s", quoteCol(c.Name, driver), c.Type)
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
		if c.Key == "PK" {
			pk = append(pk, quoteCol(c.Name, driver))
		}
		if i < len(cols)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	if len(pk) > 0 {
		fmt.Fprintf(&b, "  PRIMARY KEY (%s)\n", strings.Join(pk, ", "))
	}
	b.WriteString(");")
	return b.String()
}

// quoteCol quotes one identifier for the given driver.
func quoteCol(name, driver string) string {
	switch driver {
	case "mysql", "mariadb":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

// truncateSQL is the empty-table statement for the session's dialect.
func (ws *workspace) truncateSQL(name, quote string) string {
	switch ws.conn.Driver {
	case "mysql", "mariadb", "mssql", "postgres", "postgresql":
		return "TRUNCATE TABLE " + quote + ";"
	default: // sqlite and others have no TRUNCATE
		return "DELETE FROM " + quote + ";"
	}
}

// cloneSQL builds a "duplicate this table" script for the session's dialect.
func (ws *workspace) cloneSQL(src, srcQuote, dst, dstQuote string) string {
	switch ws.conn.Driver {
	case "mysql", "mariadb":
		return fmt.Sprintf("CREATE TABLE %s LIKE %s;\nINSERT INTO %s SELECT * FROM %s;",
			dstQuote, srcQuote, dstQuote, srcQuote)
	case "postgres", "postgresql":
		return fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL);\nINSERT INTO %s SELECT * FROM %s;",
			dstQuote, srcQuote, dstQuote, srcQuote)
	case "mssql":
		return fmt.Sprintf("SELECT * INTO %s FROM %s;", dstQuote, srcQuote)
	default: // sqlite and others
		return fmt.Sprintf("CREATE TABLE %s AS SELECT * FROM %s;", dstQuote, srcQuote)
	}
}

// cloneTable asks for a new name, then duplicates structure+data.
func (ws *workspace) cloneTable(name, quote string) {
	dialog.ShowEntryDialog("克隆 "+name, "新表名", func(dst string) {
		dst = strings.TrimSpace(dst)
		if dst == "" {
			showInfo(ws.window, "克隆", "请输入新表名。")
			return
		}
		if dst == name {
			showInfo(ws.window, "克隆", "新表名不能与原表相同。")
			return
		}
		sql := ws.cloneSQL(name, quote, dst, ws.quoteTable(dst))
		ws.runObjectSQL("克隆 "+name, sql)
	}, ws.window)
}

// confirmTruncate empties the table (structure kept).
func (ws *workspace) confirmTruncate(name, quote string) {
	dialog.NewConfirm("Truncate "+name,
		fmt.Sprintf("Delete all rows in %s? This cannot be undone.", name),
		func(ok bool) {
			if ok {
				ws.runObjectSQL("Truncate "+name, ws.truncateSQL(name, quote))
			}
		}, ws.window).Show()
}

// confirmDrop drops the table.
func (ws *workspace) confirmDrop(name, quote string) {
	dialog.NewConfirm("Delete "+name,
		fmt.Sprintf("Drop table %s? This cannot be undone.", name),
		func(ok bool) {
			if ok {
				ws.runObjectSQL("Delete "+name, "DROP TABLE "+quote+";")
			}
		}, ws.window).Show()
}

// runObjectSQL executes a DDL/DML statement and refreshes the sidebar (and the
// open table, when it is the one affected).
func (ws *workspace) runObjectSQL(title, sql string) {
	if ws.sess == nil {
		return
	}
	safeGo("objSQL "+title, func() {
		_, err := ws.sess.Query(sql, 1)
		safeDo(func() {
			if err != nil {
				showInfo(ws.window, title, err.Error())
				return
			}
			ws.loadInventory()
			if ws.tableName != "" {
				ws.reloadTable()
			}
		})
	})
}
