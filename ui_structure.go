package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// showStructureDialog displays one table's columns and indexes — the metadata
// the grid only implies. Both lists come from the dialect's catalog readers.
func (c *Client) showStructureDialog(nd *nodeData) {
	id := c.selectedOrLive()
	if _, err := c.store.OpenConn(id); err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	c.setStatus("加载 " + nd.name + " 结构 ...")

	cols, err := c.store.Columns(id, nd.db, nd.schema, nd.name)
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	// A table without indexes is normal; a catalog that refuses to answer is
	// reported but must not cost the user the column list.
	indexes, err := c.store.TableIndexes(id, nd.db, nd.schema, nd.name)
	if err != nil {
		indexes = nil
		c.setStatus("索引读取失败：" + err.Error())
	} else {
		c.setStatus("就绪")
	}

	columnRows := make([][]string, 0, len(cols))
	for _, col := range cols {
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

	indexRows := make([][]string, 0, len(indexes))
	for _, idx := range indexes {
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

	tabs := container.NewAppTabs(
		container.NewTabItem(fmt.Sprintf("字段 (%d)", len(cols)), columnTable),
		container.NewTabItem(fmt.Sprintf("索引 (%d)", len(indexes)), indexTable),
		container.NewTabItem("DDL", c.buildDDLTab(id, nd)),
	)
	d := dialog.NewCustom("结构 "+nd.name, "关闭", tabs, c.win)
	d.Resize(fyne.NewSize(680, 460))
	d.Show()
}

// buildDDLTab renders the table's reconstructed DDL with a copy button.
func (c *Client) buildDDLTab(connID string, nd *nodeData) fyne.CanvasObject {
	ddl, err := c.store.TableDDL(connID, nd.db, nd.schema, nd.name)
	if err != nil {
		ddl = "-- 无法生成建表语句：" + err.Error()
	}
	ddlEntry := widget.NewMultiLineEntry()
	ddlEntry.TextStyle = fyne.TextStyle{Monospace: true}
	ddlEntry.Wrapping = fyne.TextWrapOff
	ddlEntry.SetText(ddl)
	ddlEntry.Disable()

	copyBtn := widget.NewButtonWithIcon("复制 DDL", theme.ContentCopyIcon(), func() {
		c.win.Clipboard().SetContent(ddl)
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
