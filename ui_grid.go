package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// This file is the result-grid module: the widget, its cell states (base rows
// layered with the edit queue), column widths, sorting, copying and the
// exports of the displayed result.

// copySelectedRow copies the selected row to the clipboard as tab-separated
// values, ready to paste into a spreadsheet.
func (c *Client) copySelectedRow() {
	if !c.hasCell {
		return
	}
	values := make([]string, 0, len(c.gridHeader))
	for col := range c.gridHeader {
		text, _ := c.gridCellState(c.selectedCell.Row, col)
		values = append(values, text)
	}
	fyne.CurrentApp().Clipboard().SetContent(strings.Join(values, "\t"))
	c.setStatus("已复制整行（制表符分隔）")
}

// exportGrid saves the current result grid as CSV (default) or JSON when the
// chosen file name ends in .json. Selection is written as displayed, so a
// sorted grid exports in sorted order.
func (c *Client) exportGrid() {
	if len(c.gridHeader) == 0 {
		c.setStatus("没有可导出的结果")
		return
	}
	d := dialog.NewFileSave(func(f fyne.URIWriteCloser, err error) {
		if err != nil {
			c.setStatus("导出失败：" + err.Error())
			return
		}
		if f == nil {
			return
		}
		defer f.Close()
		var werr error
		switch f.URI().Extension() {
		case ".json":
			werr = db.WriteJSON(f, c.gridHeader, c.gridRows)
		case ".sql":
			werr = c.writeInsertScript(f)
		case ".xlsx":
			werr = c.writeWorkbookFile(f)
		default:
			werr = db.WriteCSV(f, c.gridHeader, c.gridRows)
		}
		if werr != nil {
			c.setStatus("导出失败：" + werr.Error())
			return
		}
		c.setStatus(fmt.Sprintf("已导出 %d 行到 %s", len(c.gridRows), f.URI().Name()))
	}, c.win)
	d.SetFileName("result.csv")
	d.Show()
}

// writeInsertScript renders the current raw result as INSERT statements for
// the table the query read from. Only a single-table SELECT * can be turned
// back into INSERTs; anything else is refused with a reason.
func (c *Client) writeInsertScript(f fyne.URIWriteCloser) error {
	if c.sourceRef.Table == "" {
		return fmt.Errorf("INSERT 导出需要单表的 SELECT * 查询")
	}
	conn, err := c.store.OpenConn(c.currentID)
	if err != nil {
		return err
	}
	typ := conn.Config.Type
	schema := db.DefaultSchema(typ, c.sourceRef.Schema)
	name, err := db.TableName(typ, c.sourceRef.Database, schema, c.sourceRef.Table)
	if err != nil {
		return err
	}
	cols, err := c.store.Columns(c.currentID, c.sourceRef.Database, schema, c.sourceRef.Table)
	if err != nil {
		return err
	}
	types := make(map[string]string, len(cols))
	for _, col := range cols {
		types[strings.ToLower(col.Name)] = col.Type
	}
	script, err := db.InsertScript(typ, name, c.sourceRef.Table, c.gridCols, types, c.lastRows)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(script))
	return err
}

// copyCell copies the selected cell's full value to the clipboard.
func (c *Client) copyCell() {
	if !c.hasCell {
		return
	}
	text := c.gridCell(c.selectedCell)
	if text == "" {
		return
	}
	fyne.CurrentApp().Clipboard().SetContent(text)
	c.setStatus("已复制单元格内容")
}

// sortGrid sorts the current result rows by the tapped column. Numbers compare
// numerically, text lexicographically; NULLs always sort last regardless of
// direction. Tapping the sorted column again flips ascending/descending.
func (c *Client) sortGrid(col int) {
	if col < 0 || col >= len(c.gridHeader) {
		return
	}
	if c.edits.any() {
		c.setStatus("有未保存的更改，先保存或放弃再排序")
		return
	}
	if c.sortCol == col {
		c.sortAsc = !c.sortAsc
	} else {
		c.sortCol = col
		c.sortAsc = true
	}
	asc := c.sortAsc
	sort.SliceStable(c.gridRows, func(i, j int) bool {
		if col >= len(c.gridRows[i]) || col >= len(c.gridRows[j]) {
			return false
		}
		a, b := c.gridRows[i][col], c.gridRows[j][col]
		aNull, bNull := a == "NULL", b == "NULL"
		if aNull != bNull {
			return !aNull // non-NULL first; NULLs always sink to the bottom
		}
		if aNull {
			return false
		}
		fa, ea := strconv.ParseFloat(a, 64)
		fb, eb := strconv.ParseFloat(b, 64)
		if ea == nil && eb == nil {
			if asc {
				return fa < fb
			}
			return fa > fb
		}
		if asc {
			return a < b
		}
		return a > b
	})
	if c.resultGrid != nil {
		// Row order just changed, so any active search must re-collect hits.
		c.rescanGridSearch()
	}
}

// gridDims reports the active table dimensions: base result rows plus any
// queued new rows, × the header columns.
func (c *Client) gridDims() (int, int) {
	rows := len(c.gridRows)
	if c.edits != nil {
		rows += len(c.edits.inserts)
	}
	return rows, len(c.gridHeader)
}

// gridCell returns the effective display text of one cell.
func (c *Client) gridCell(id widget.TableCellID) string {
	text, _ := c.gridCellState(id.Row, id.Col)
	return text
}

// applyColumnWidths sizes every column to fit its content (header + sampled
// rows), capped to a sane range so one long blob can't monopolise the view.
func (c *Client) applyColumnWidths(header []string, rows [][]string) {
	for i := 0; i < len(header); i++ {
		c.resultGrid.SetColumnWidth(i, c.measureColumn(header[i], rows, i))
	}
}

const (
	minColWidth = 70.0
	maxColWidth = 240.0
	colPadding  = 20.0
	measureRows = 50 // cap sampled rows for width estimation
)

// measureColumn sizes a column from the widest header/data cell. To stay fast
// (no thousands of MeasureText calls), widths are estimated from rune counts
// using a single cached per-character width; CJK runes count as two columns.
func (c *Client) measureColumn(header string, rows [][]string, col int) float32 {
	widest := displayWidth(header)
	max := measureRows
	if max > len(rows) {
		max = len(rows)
	}
	for r := 0; r < max; r++ {
		if col < len(rows[r]) {
			if n := displayWidth(rows[r][col]); n > widest {
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

// charWidth measures a representative narrow character once and caches it.
var cachedCharWidth float32 = -1

func charWidth() float32 {
	if cachedCharWidth < 0 {
		cachedCharWidth = fyne.MeasureText("8888888888", theme.TextSize(), fyne.TextStyle{}).Width / 10
		if cachedCharWidth <= 0 {
			cachedCharWidth = 8
		}
	}
	return cachedCharWidth
}

// displayWidth returns a monospace-style cell width: ASCII runes count 1,
// wide runes (CJK etc.) count 2, matching typical terminal/DB grid heuristics.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if utf8.RuneLen(r) >= 3 {
			w += 2 // CJK / full-width
		} else {
			w++
		}
	}
	return w
}

// renderResult turns a db.QueryResult into the grid + status line.
func (c *Client) renderResult(res db.QueryResult) {
	if res.Error != "" {
		c.setStatus("错误：" + res.Error)
		return
	}
	if len(res.Columns) == 0 {
		msg := fmt.Sprintf("完成：影响 %d 行，耗时 %.2fs", res.AffectedRows, res.ExecutionTime)
		if res.LastInsertID > 0 {
			msg = fmt.Sprintf("完成：影响 %d 行，自增ID=%d，耗时 %.2fs",
				res.AffectedRows, res.LastInsertID, res.ExecutionTime)
		}
		c.setStatus(msg)
		return
	}

	header := make([]string, len(res.Columns))
	for i, name := range res.Columns {
		if res.ColTypes[i] != "" {
			header[i] = fmt.Sprintf("%s (%s)", name, res.ColTypes[i])
		} else {
			header[i] = name
		}
	}
	rows := make([][]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		row := make([]string, len(res.Columns))
		for i := range r {
			row[i] = db.ValueString(r[i])
		}
		rows = append(rows, row)
	}
	c.gridHeader = header
	c.gridRows = rows
	c.gridCols = res.Columns
	c.gridColTypes = res.ColTypes
	c.hasCell = false
	c.sortCol = -1
	c.clearGridSearch()
	if c.cellView != nil {
		c.cellView.SetText("")
	}
	c.applyColumnWidths(header, rows)
	c.resultGrid.Refresh()
	c.updateEditButtons()
	msg := fmt.Sprintf("返回 %d 行 × %d 列，耗时 %.2fs", res.RowCount, len(res.Columns), res.ExecutionTime)
	if c.gridEditable() {
		msg += " · 可编辑表 " + c.sourceRef.Table
	} else if res.RowCount > 0 {
		msg += " · 只读（编辑需 SELECT * 单表）"
	}
	c.setStatus(msg)
}
