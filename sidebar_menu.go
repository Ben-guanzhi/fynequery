package main

// Left-sidebar item context menu (TablePlus "Item actions"): open, view
// structure, copy the object's name or a SQL script, and clone/truncate/drop.
// Triggered by a right-click on a tree item (see treeItem.TappedSecondary).

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/dbcore/dbcore/sqltext"

	"fynequery/db"
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
	if ka, ok := db.AsKeyAdmin(ws.sess); ok {
		ws.showKeyMenu(name, canvas, pos, ka)
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

// showKeyMenu is the KV variant of the item menu: a key has no structure, DDL
// or clone semantics, but it does have a type, a TTL and its own name.
func (ws *workspace) showKeyMenu(name string, canvas fyne.Canvas, pos fyne.Position, ka db.KeyAdmin) {
	pinItem := fyne.NewMenuItem("置顶", func() { ws.side.togglePin(name) })
	if ws.side != nil && ws.side.isPinned(name) {
		pinItem = fyne.NewMenuItem("取消置顶", func() { ws.side.togglePin(name) })
	}
	menu := fyne.NewMenu("",
		pinItem,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("打开", func() { ws.openTable(name) }),
		fyne.NewMenuItem("复制键名", func() { ws.copyText(name) }),
		fyne.NewMenuItem("键信息…", func() { ws.showKeyMeta(name, ka) }),
		fyne.NewMenuItem("设置 TTL…", func() { ws.setKeyTTL(name, ka) }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("重命名…", func() { ws.renameKey(name, ka) }),
		fyne.NewMenuItem("删除键…", func() { ws.deleteKey(name, ka) }),
	)
	widget.ShowPopUpMenuAtPosition(menu, canvas, pos)
}

// showKeyMeta reports the key's type and remaining lifetime.
func (ws *workspace) showKeyMeta(name string, ka db.KeyAdmin) {
	safeGo("keyMeta "+name, func() {
		meta, err := ka.KeyMeta(name)
		safeDo(func() {
			if err != nil {
				showInfo(ws.window, "键信息", err.Error())
				return
			}
			ttl := "永不过期"
			if meta.HasTTL {
				ttl = meta.TTL.Round(time.Second).String()
			}
			showInfo(ws.window, "键信息 — "+name,
				fmt.Sprintf("键：%s\n类型：%s\nTTL：%s", name, meta.Kind, ttl))
		})
	})
}

// setKeyTTL changes the key's expiry. The input is a number of seconds; 0
// removes the expiry (PERSIST), so the key never expires.
func (ws *workspace) setKeyTTL(name string, ka db.KeyAdmin) {
	dialog.ShowEntryDialog("设置 TTL — "+name, "过期秒数（0 = 永不过期）", func(input string) {
		input = strings.TrimSpace(input)
		secs, err := strconv.ParseInt(input, 10, 64)
		if err != nil {
			showInfo(ws.window, "设置 TTL", "请输入整数秒数。")
			return
		}
		if secs < 0 {
			showInfo(ws.window, "设置 TTL", "秒数不能为负数；0 表示永不过期。")
			return
		}
		ttl := time.Duration(secs) * time.Second
		if secs > 0 && ttl <= 0 {
			showInfo(ws.window, "设置 TTL", "过期时间过大。")
			return
		}
		desc := fmt.Sprintf("PERSIST %q", name)
		if secs > 0 {
			desc = fmt.Sprintf("PEXPIRE %q %d", name, ttl.Milliseconds())
		}
		gateSafeMode(ws.window, ws.conn, desc, func() {
			safeGo("setKeyTTL "+name, func() {
				err := ka.SetKeyTTL(name, ttl)
				safeDo(func() {
					if err != nil {
						showInfo(ws.window, "设置 TTL", err.Error())
						return
					}
					ws.logf("已设置键 %s 的 TTL：%s", name, desc)
					ws.showKeyMeta(name, ka)
				})
			})
		})
	}, ws.window)
}

// renameKey asks for a new name and renames the key. The driver refuses to
// overwrite an existing key (RENAMENX), so a typo cannot destroy another key.
func (ws *workspace) renameKey(name string, ka db.KeyAdmin) {
	dialog.ShowEntryDialog("重命名键 "+name, "新键名", func(dst string) {
		dst = strings.TrimSpace(dst)
		if dst == "" {
			showInfo(ws.window, "重命名键", "请输入新键名。")
			return
		}
		if dst == name {
			showInfo(ws.window, "重命名键", "新键名不能与原键相同。")
			return
		}
		gateSafeMode(ws.window, ws.conn, fmt.Sprintf("RENAME %q %q", name, dst), func() {
			safeGo("renameKey "+name, func() {
				err := ka.RenameKey(name, dst)
				safeDo(func() {
					if err != nil {
						showInfo(ws.window, "重命名键", err.Error())
						return
					}
					ws.closeDataTabNamed(name)
					ws.loadInventory()
					ws.openTable(dst)
					ws.logf("重命名键 %s → %s", name, dst)
				})
			})
		})
	}, ws.window)
}

// deleteKey asks for confirmation and deletes the key (DEL).
func (ws *workspace) deleteKey(name string, ka db.KeyAdmin) {
	dialog.NewConfirm("删除键 "+name,
		fmt.Sprintf("删除键 %s？此操作不可撤销。", name),
		func(ok bool) {
			if !ok {
				return
			}
			gateSafeMode(ws.window, ws.conn, fmt.Sprintf("DEL %q", name), func() {
				safeGo("deleteKey "+name, func() {
					err := ka.DeleteKey(name)
					safeDo(func() {
						if err != nil {
							showInfo(ws.window, "删除键", err.Error())
							return
						}
						ws.closeDataTabNamed(name)
						ws.loadInventory()
						ws.logf("已删除键 %s", name)
					})
				})
			})
		}, ws.window).Show()
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
	if q, err := sqltext.QuoteIdent(driver, name); err == nil {
		return q
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
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
