package main

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// structureData is one table's catalog as read from the server: the four lists
// the dialog shows, each with the error that would have left it empty. It is
// filled off the event thread and rendered on it.
type structureData struct {
	connID   string
	dbType   string
	database string
	table    string
	columns  []db.ColumnInfo
	indexes  []db.IndexInfo
	fks      []db.ForeignKeyInfo
	ddl      string
	colsErr  error
	idxErr   error
	fkErr    error
	ddlErr   error
}

// showStructureDialog displays one table's columns and indexes — the metadata
// the grid only implies. All four lists come from the dialect's catalog readers,
// so they are read off the event thread: on a remote server the old synchronous
// version left the window unpainted before the dialog ever appeared.
func (c *Client) showStructureDialog(nd *nodeData) {
	id := c.selectedOrLive()
	conn, err := c.store.OpenConn(id)
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	c.setStatus("加载 " + nd.name + " 结构 ...")
	data := structureData{
		connID:   id,
		dbType:   conn.Config.Type,
		database: nd.db,
		table:    nd.name,
	}
	schema := nd.schema
	c.background(func(ctx context.Context) {
		data.columns, data.colsErr = c.store.Columns(ctx, id, nd.db, schema, nd.name)
		// A table without indexes is normal; a catalog that refuses to answer is
		// reported but must not cost the user the column list.
		data.indexes, data.idxErr = c.store.TableIndexes(ctx, id, nd.db, schema, nd.name)
		// Foreign keys are the third thing a grid cannot show: which constraint
		// points where, and what the engine does to the child rows on delete or
		// update. Same rule as the index list - a catalog that refuses to answer
		// costs this tab its rows, not the dialog.
		data.fks, data.fkErr = c.foreignKeys(ctx, id, nd.db, schema, nd.name)
		data.ddl, data.ddlErr = c.store.TableDDL(ctx, id, nd.db, schema, nd.name)
	}, func() { c.showStructureDialogBody(data) })
}

// showStructureDialogBody turns the fetched catalog rows into the tabbed
// dialog. UI thread only.
func (c *Client) showStructureDialogBody(data structureData) {
	columnRows := make([][]string, 0, len(data.columns))
	for _, col := range data.columns {
		role := ""
		if col.IsPrimaryKey {
			role = "PK"
		}
		nullable := "NO"
		if col.Nullable {
			nullable = "YES"
		}
		columnRows = append(columnRows, []string{
			col.Name, col.Type, nullable, role, col.DefaultValue, col.Comment,
		})
	}
	columnTable := newMetaTable(
		[]string{"字段", "类型", "可空", "键", "默认值", "注释"}, columnRows)

	indexRows := make([][]string, 0, len(data.indexes))
	for _, idx := range data.indexes {
		flags := ""
		if idx.Primary {
			flags = "PK"
		} else if idx.Unique {
			flags = "UNIQUE"
		}
		indexRows = append(indexRows, []string{
			idx.Name, idx.Kind, flags, strings.Join(idx.Columns, ", "),
		})
	}
	indexTable := newMetaTable(
		[]string{"索引", "类型", "约束", "字段"}, indexRows)

	fkRows := make([][]string, 0, len(data.fks))
	for _, fk := range data.fks {
		fkRows = append(fkRows, []string{
			fk.Name,
			strings.Join(fk.Columns, ", "),
			db.RefName(data.dbType, data.database, fk),
			strings.Join(fk.RefColumns, ", "),
			fk.OnDelete, fk.OnUpdate,
		})
	}
	fkTable := newMetaTable(
		[]string{"约束", "本表字段", "引用表", "引用字段", "ON DELETE", "ON UPDATE"}, fkRows)

	if data.colsErr != nil {
		dialog.ShowError(data.colsErr, c.win)
		return
	}
	switch {
	case data.idxErr != nil:
		c.setStatus("索引读取失败：" + data.idxErr.Error())
	case data.fkErr != nil:
		c.setStatus("外键读取失败：" + data.fkErr.Error())
	default:
		c.setStatus("就绪")
	}

	tabs := container.NewAppTabs(
		container.NewTabItem(fmt.Sprintf("字段 (%d)", len(data.columns)), columnTable),
		container.NewTabItem(fmt.Sprintf("索引 (%d)", len(data.indexes)), indexTable),
		container.NewTabItem(fmt.Sprintf("外键 (%d)", len(data.fks)), fkTable),
		container.NewTabItem("DDL", c.ddlTab(data.ddl, data.ddlErr)),
	)
	d := dialog.NewCustom("结构 "+data.table, "关闭", tabs, c.win)
	d.Resize(fyne.NewSize(680, 460))
	d.Show()
}

// ddlTab renders the table's reconstructed DDL with a copy button. A catalog
// that refuses the DDL costs this tab its text, not the dialog.
func (c *Client) ddlTab(ddl string, err error) fyne.CanvasObject {
	text := ddl
	if err != nil {
		text = "-- 无法生成建表语句：" + err.Error()
	}
	ddlEntry := widget.NewMultiLineEntry()
	ddlEntry.TextStyle = fyne.TextStyle{Monospace: true}
	ddlEntry.Wrapping = fyne.TextWrapOff
	ddlEntry.SetText(text)
	ddlEntry.Disable()

	copyBtn := widget.NewButtonWithIcon("复制 DDL", theme.ContentCopyIcon(), func() {
		c.win.Clipboard().SetContent(text)
		c.setStatus("已复制建表语句")
	})
	return container.NewBorder(container.NewHBox(copyBtn), nil, nil, nil, ddlEntry)
}

// newMetaTable builds a read-only grid with a header row and content-fitted
// column widths, for small metadata listings.
func newMetaTable(headers []string, rows [][]string) *widget.Table {
	t := widget.NewTable(
		func() (int, int) { return len(rows), len(headers) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.TableCellID, obj fyne.CanvasObject) {
			label := obj.(*widget.Label)
			label.Truncation = fyne.TextTruncateEllipsis
			label.TextStyle = fyne.TextStyle{}
			label.Importance = widget.MediumImportance
			text := ""
			if id.Row < len(rows) && id.Col < len(rows[id.Row]) {
				text = rows[id.Row][id.Col]
			}
			label.SetText(text)
		},
	)
	t.ShowHeaderRow = true
	t.CreateHeader = func() fyne.CanvasObject { return widget.NewButton("", nil) }
	t.UpdateHeader = func(id widget.TableCellID, obj fyne.CanvasObject) {
		btn, ok := obj.(*widget.Button)
		if !ok {
			return
		}
		btn.Importance = widget.LowImportance
		btn.Alignment = widget.ButtonAlignLeading
		btn.OnTapped = nil
		switch {
		case id.Row == -1 && id.Col >= 0 && id.Col < len(headers):
			btn.SetText(headers[id.Col])
		case id.Col == -1 && id.Row >= 0:
			btn.SetText(fmt.Sprintf("%d", id.Row+1))
		default:
			btn.SetText("")
		}
	}
	for i, h := range headers {
		t.SetColumnWidth(i, staticColumnWidth(h, rows, i))
	}
	return t
}

// staticColumnWidth fits a metadata column to its widest cell, clamped like
// the result grid's widths.
func staticColumnWidth(header string, rows [][]string, col int) float32 {
	widest := displayWidth(header)
	for _, row := range rows {
		if col < len(row) {
			if n := displayWidth(row[col]); n > widest {
				widest = n
			}
		}
	}
	w := float32(widest)*charWidth() + colPadding
	if w < minColWidth {
		w = minColWidth
	}
	if w > maxColWidth {
		w = maxColWidth
	}
	return w
}
