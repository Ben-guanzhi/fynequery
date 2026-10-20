package main

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Ben-guanzhi/TableProGo/tableprosql"

	"tablepro-fyne/internal/db"
)

// This file is the result-grid module: the widget, its cell states (base rows
// layered with the edit queue), column widths, sorting, copying and the
// exports of the displayed result.

// copySelectedRow copies the selected row to the clipboard as tab-separated
// values, ready to paste into a spreadsheet.
func (c *Client) copySelectedRow() {
	if !c.grid.hasCell {
		return
	}
	values := make([]string, 0, len(c.grid.header))
	for col := range c.grid.header {
		text, _ := c.gridCellState(c.grid.cell.Row, col)
		values = append(values, text)
	}
	fyne.CurrentApp().Clipboard().SetContent(strings.Join(values, "\t"))
	c.setStatus("已复制整行（制表符分隔）")
}

// exportGrid saves the current result grid as CSV (default) or JSON when the
// chosen file name ends in .json. Selection is written as displayed, so a
// sorted grid exports in sorted order.
func (c *Client) exportGrid() {
	if len(c.grid.header) == 0 {
		c.setStatusWarn("没有可导出的结果")
		return
	}
	d := dialog.NewFileSave(func(f fyne.URIWriteCloser, err error) {
		if err != nil {
			c.setStatusError("导出失败：" + err.Error())
			return
		}
		if f == nil {
			return
		}
		name := f.URI().Name()
		report := func(werr error) {
			if werr != nil {
				c.setStatusError("导出失败：" + werr.Error())
				return
			}
			c.setStatus(fmt.Sprintf("已导出 %d 行到 %s", len(c.grid.rows), name))
		}
		if f.URI().Extension() != ".sql" {
			// The other formats render from rows already in memory: nothing is
			// waited on, so the file can be closed right here.
			defer f.Close()
			report(c.writeGridFile(f))
			return
		}
		// The INSERT export is the one format that needs the catalog, for the
		// column types. That read goes off the event thread, and its callback
		// writes and closes the file - the handle may not be closed early. A
		// second export abandons the first (and its open file) up front.
		c.releaseExport()
		var (
			dbType string
			types  map[string]string
			tErr   error
		)
		connID, ref := c.currentID, c.grid.src
		c.exportFile = f
		c.setStatus("导出中 ...")
		c.exportCancel = c.background(func(ctx context.Context) {
			dbType, types, tErr = c.insertColumnTypes(ctx, connID, ref.Database, ref.Schema, ref.Table)
		}, func() {
			defer c.releaseExport()
			if tErr != nil {
				report(tErr)
				return
			}
			report(c.writeInsertScript(f, dbType, types))
		})
	}, c.win)
	d.SetFileName("result.csv")
	d.Show()
}

// releaseExport abandons a still-running INSERT export: it drops the catalog
// read and closes the file it has open, then clears both handles. A finished
// export clears them in its own callback, so this is a no-op there.
func (c *Client) releaseExport() {
	if c.exportCancel != nil {
		c.exportCancel()
		c.exportCancel = nil
	}
	if c.exportFile != nil {
		c.exportFile.Close()
		c.exportFile = nil
	}
}

// writeGridFile renders the result into the chosen file format. Everything it
// needs is already in memory - which is why the INSERT export, whose types come
// from the catalog, is handled by the caller instead.
func (c *Client) writeGridFile(f fyne.URIWriteCloser) error {
	switch f.URI().Extension() {
	case ".json":
		return db.WriteJSON(f, c.grid.header, c.grid.rows)
	case ".xlsx":
		return c.writeWorkbookFile(f)
	default:
		// The BOM is what makes Excel on Windows read the file as UTF-8;
		// without it a plain UTF-8 CSV is assumed ANSI and Chinese text in
		// every cell turns to mojibake. JSON deliberately gets none - parsers
		// reject it.
		if _, err := f.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
			return err
		}
		return db.WriteCSV(f, c.grid.header, c.grid.rows)
	}
}

// insertColumnTypes resolves what the INSERT export cannot know from the grid:
// the engine and the type of every column. The types are a catalog read, which
// makes this the only round trip an export makes.
func (c *Client) insertColumnTypes(ctx context.Context, connID, database, schema, table string) (string, map[string]string, error) {
	conn, err := c.store.OpenConn(connID)
	if err != nil {
		return "", nil, err
	}
	typ := conn.Config.Type
	schema = db.DefaultSchema(typ, schema)
	cols, err := c.store.Columns(ctx, connID, database, schema, table)
	if err != nil {
		return "", nil, err
	}
	types := make(map[string]string, len(cols))
	for _, col := range cols {
		types[strings.ToLower(col.Name)] = col.Type
	}
	return typ, types, nil
}

// writeInsertScript renders the current raw result as INSERT statements and
// writes them into the chosen file.
func (c *Client) writeInsertScript(f fyne.URIWriteCloser, dbType string, types map[string]string) error {
	script, err := c.renderInsertScript(dbType, types)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(script))
	return err
}

// renderInsertScript is the text behind both 「导出 → INSERT」 and 「复制为 →
// INSERT」. Only a single-table SELECT * can be turned back into INSERTs; the
// engine and the column types come from insertColumnTypes - the grid holds
// values, not declarations.
func (c *Client) renderInsertScript(dbType string, types map[string]string) (string, error) {
	src := c.grid.src
	if src.Table == "" {
		return "", fmt.Errorf("INSERT 需要单表的 SELECT * 查询")
	}
	name, err := db.TableName(dbType, src.Database,
		db.DefaultSchema(dbType, src.Schema), src.Table)
	if err != nil {
		return "", err
	}
	return db.InsertScript(dbType, name, src.Table, c.grid.cols, types, c.grid.lastRows)
}

// copyResultAs puts the whole grid on the clipboard in one of the export
// formats. CSV keeps the header and drops the BOM: that prefix exists so Excel
// reads a FILE as UTF-8, and the clipboard carries text, not bytes. The INSERT
// form needs each column's declared type, so it is the one round trip in this
// path - it runs through background, and a second copy abandons the first.
func (c *Client) copyResultAs(kind string) {
	if len(c.grid.header) == 0 {
		c.setStatusWarn("没有可复制的结果")
		return
	}
	if kind == "insert" {
		if c.grid.src.Table == "" {
			// Refused before the round trip: with no table name there is
			// nothing to render, and guessing one would write into a table
			// nobody chose.
			c.setStatusWarn("复制为 INSERT 需要单表的 SELECT * 查询")
			return
		}
		c.releaseExport()
		connID, ref := c.currentID, c.grid.src
		var (
			dbType string
			types  map[string]string
			err    error
			script string
		)
		c.setStatus("生成 INSERT 语句 ...")
		c.exportCancel = c.background(func(ctx context.Context) {
			dbType, types, err = c.insertColumnTypes(ctx, connID, ref.Database, ref.Schema, ref.Table)
			if err == nil {
				script, err = c.renderInsertScript(dbType, types)
			}
		}, func() {
			defer c.releaseExport()
			if err != nil {
				c.setStatusError("复制失败：" + err.Error())
				return
			}
			fyne.CurrentApp().Clipboard().SetContent(script)
			c.setStatus(fmt.Sprintf("已复制 %d 行（INSERT）", len(c.grid.rows)))
		})
		return
	}
	var buf bytes.Buffer
	var err error
	switch kind {
	case "json":
		err = db.WriteJSON(&buf, c.grid.header, c.grid.rows)
	default:
		err = db.WriteCSV(&buf, c.grid.header, c.grid.rows)
	}
	if err != nil {
		c.setStatusError("复制失败：" + err.Error())
		return
	}
	fyne.CurrentApp().Clipboard().SetContent(buf.String())
	c.setStatus(fmt.Sprintf("已复制 %d 行（%s）", len(c.grid.rows), strings.ToUpper(kind)))
}

// showCopyAsMenu opens the 复制为 menu under its button. The three entries are
// the export formats the file dialog offers, minus the dialog.
func (c *Client) showCopyAsMenu(anchor fyne.CanvasObject) {
	menu := fyne.NewMenu("",
		fyne.NewMenuItem("CSV（带表头）", func() { c.copyResultAs("csv") }),
		fyne.NewMenuItem("JSON", func() { c.copyResultAs("json") }),
		fyne.NewMenuItem("INSERT 语句", func() { c.copyResultAs("insert") }),
	)
	pop := widget.NewPopUpMenu(menu, c.win.Canvas())
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(anchor)
	pos.Y += anchor.Size().Height
	pop.ShowAtPosition(pos)
}

// copyCell copies the selected cell's full value to the clipboard.
func (c *Client) copyCell() {
	if !c.grid.hasCell {
		return
	}
	text := c.gridCell(c.grid.cell)
	if text == "" {
		return
	}
	fyne.CurrentApp().Clipboard().SetContent(text)
	c.setStatus("已复制单元格内容")
}

// sortGrid sorts the current result rows by the tapped column. NULLs always
// sort last regardless of direction. Tapping the sorted column again flips
// ascending/descending. Numbers compare numerically only when the column's
// declared type is numeric (or unknown, when the grid holds no types): a text
// column that happens to contain digits must not suddenly reorder them - for
// "10", "2", "abc" the natural reading of a text column is lexicographic.
//
// The same permutation is applied to lastRows: the edit queue and the NULL
// test address base rows by display index, so a sort that moved the strings
// but not the raw rows would point UPDATE/DELETE WHERE clauses at a
// neighbouring row.
func (c *Client) sortGrid(col int) {
	if col < 0 || col >= len(c.grid.header) {
		return
	}
	if c.grid.edits.any() {
		c.setStatusWarn("有未保存的更改，先保存或放弃再排序")
		return
	}
	if c.grid.sortCol == col {
		c.grid.sortAsc = !c.grid.sortAsc
	} else {
		c.grid.sortCol = col
		c.grid.sortAsc = true
	}
	asc := c.grid.sortAsc
	numeric := true
	if col < len(c.grid.colTypes) && c.grid.colTypes[col] != "" {
		numeric = tableprosql.NewColumnType(c.grid.colTypes[col]).IsNumeric()
	}
	idx := make([]int, len(c.grid.rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(x, y int) bool {
		i, j := idx[x], idx[y]
		if col >= len(c.grid.rows[i]) || col >= len(c.grid.rows[j]) {
			return false
		}
		a, b := c.grid.rows[i][col], c.grid.rows[j][col]
		aNull, bNull := c.cellIsNull(i, col), c.cellIsNull(j, col)
		if aNull != bNull {
			return !aNull // non-NULL first; NULLs always sink to the bottom
		}
		if aNull {
			return false
		}
		if numeric {
			fa, ea := strconv.ParseFloat(a, 64)
			fb, eb := strconv.ParseFloat(b, 64)
			if ea == nil && eb == nil {
				if asc {
					return fa < fb
				}
				return fa > fb
			}
		}
		if asc {
			return a < b
		}
		return a > b
	})
	sorted := make([][]string, len(idx))
	for x, i := range idx {
		sorted[x] = c.grid.rows[i]
	}
	c.grid.rows = sorted
	if len(c.grid.lastRows) == len(idx) {
		sortedRaw := make([][]interface{}, len(idx))
		for x, i := range idx {
			sortedRaw[x] = c.grid.lastRows[i]
		}
		c.grid.lastRows = sortedRaw
	}
	if c.grid.table != nil {
		// Row order just changed, so any active search must re-collect hits.
		c.rescanGridSearch()
	}
}

// gridDims reports the active table dimensions: base result rows plus any
// queued new rows, × the header columns.
func (c *Client) gridDims() (int, int) {
	rows := len(c.grid.rows)
	if c.grid.edits != nil {
		rows += len(c.grid.edits.inserts)
	}
	return rows, len(c.grid.header)
}

// applyGridRowHeight pins every visible row to one fixed height, so the grid
// reads as an even table (the TablePlus look) instead of following each cell's
// content. It is called wherever the row count can change.
func (c *Client) applyGridRowHeight() {
	const gridRowHeight = 28.0
	if c.grid.table == nil {
		return
	}
	rows, _ := c.gridDims()
	for r := 0; r < rows; r++ {
		c.grid.table.SetRowHeight(r, gridRowHeight)
	}
}

// gridCell returns the effective display text of one cell.
func (c *Client) gridCell(id widget.TableCellID) string {
	text, _ := c.gridCellState(id.Row, id.Col)
	return text
}

// cellIsNull reports whether the base result value behind a cell is a real SQL
// NULL. The displayed text is lossy - a text column can legitimately hold the
// string "NULL" - so only the raw result rows are a reliable answer.
func (c *Client) cellIsNull(row, col int) bool {
	if row < 0 || col < 0 || row >= len(c.grid.lastRows) {
		return false
	}
	r := c.grid.lastRows[row]
	return col < len(r) && r[col] == nil
}

// colAlignsRight reports whether a column's values render right-aligned:
// numeric columns only, matching the sort rule that treats a column as numbers
// only when its declared type says so.
func (c *Client) colAlignsRight(col int) bool {
	if col < 0 || col >= len(c.grid.alignRight) {
		return false
	}
	return c.grid.alignRight[col]
}

// alignRightFor decides which columns render right-aligned: exactly the ones
// whose declared type is numeric, so a text column that happens to hold digits
// keeps the left edge it was meant for. Unknown types align left.
func alignRightFor(colTypes []string) []bool {
	out := make([]bool, len(colTypes))
	for i, t := range colTypes {
		out[i] = t != "" && tableprosql.NewColumnType(t).IsNumeric()
	}
	return out
}

// applyColumnWidths sizes every column to fit its content (header + sampled
// rows), capped to a sane range so one long blob can't monopolise the view.
func (c *Client) applyColumnWidths(header []string, rows [][]string) {
	for i := 0; i < len(header); i++ {
		c.grid.table.SetColumnWidth(i, c.measureColumn(header[i], rows, i))
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
		c.setStatusError("错误：" + res.Error)
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
	msg := fmt.Sprintf("返回 %d 行 × %d 列，耗时 %.2fs", res.RowCount, len(res.Columns), res.ExecutionTime)
	if c.gridEditable() {
		msg += " · 可编辑表 " + c.grid.src.Table
	} else if res.RowCount > 0 {
		msg += " · 只读（编辑需 SELECT * 单表）"
	}

	c.grid.header = header
	c.grid.cols = res.Columns
	c.grid.colTypes = res.ColTypes
	c.grid.alignRight = alignRightFor(res.ColTypes)
	c.grid.hasCell = false
	c.grid.sortCol = -1
	c.clearGridSearch()
	if c.grid.view != nil {
		c.grid.view.SetText("")
	}
	// 大结果走流式：先按全量算列宽，行再分批推进（见 renderRowsStreamed）。
	if len(rows) > streamThreshold && c.dispatch != nil {
		c.applyColumnWidths(header, rows)
		c.grid.rows = nil
		c.grid.table.Refresh()
		c.renderRowsStreamed(rows, msg)
		return
	}
	c.grid.rows = rows
	c.applyColumnWidths(header, rows)
	c.applyGridRowHeight()
	c.grid.table.Refresh()
	c.updateEditButtons()
	c.setStatus(msg)
}

// 流式渲染的阈值与批大小（移植自内置版 query.go:480-506 的分批 + 代际守卫）。
//
// 独立版默认限 1000 行、且分页走服务端，所以只有「无限制」之类的大结果才会
// 越过阈值走这条路径；小结果仍一次性装载，行为与移植前完全一致（既有测试全
// 部走这条同步路径）。
const (
	streamThreshold = 2000
	streamBatch     = 1000
)

// renderRowsStreamed 把大结果分批推进网格：每批 append 一次并 Refresh，让界面
// 边装边画，而不是整批装完才动。带代际守卫：新一轮开跑后，旧批次直接丢弃。
func (c *Client) renderRowsStreamed(rows [][]string, msg string) {
	c.mu.Lock()
	gen := c.runGen
	c.mu.Unlock()

	for from := 0; from < len(rows); from += streamBatch {
		start := from
		c.dispatch(func() {
			c.mu.Lock()
			stale := c.runGen != gen
			c.mu.Unlock()
			if stale {
				return // 已被新一轮取代，这批不再落地
			}
			to := start + streamBatch
			if to > len(rows) {
				to = len(rows)
			}
			c.grid.rows = append(c.grid.rows, rows[start:to]...)
			c.applyGridRowHeight()
			c.grid.table.Refresh()
		})
	}
	c.updateEditButtons()
	c.setStatus(msg)
}
