package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// This file assembles the query page: the toolbar, query tabs, the editor
// area and the result grid, plus the execution entry point (runCurrent).

// buildQueryView assembles the editor/grid half of the main window.
func (c *Client) buildQueryView() fyne.CanvasObject {
	// Query tabs: several independent editors, one shared result grid.
	// Everything downstream addresses the active editor through c.queryEntry,
	// so switching tabs is just re-pointing that alias.
	c.sugBar = container.NewVBox()
	c.sugBar.Hide()
	c.tabSeq = 0
	c.qTabs = container.NewAppTabs()
	c.qTabs.OnSelected = func(item *container.TabItem) { c.syncActiveTab(item) }
	c.addQueryTab()

	c.dbSel = widget.NewSelect(nil, func(db string) {
		c.currentDB = db
		c.expandDatabaseInTree(db)
	})

	runBtn := widget.NewButtonWithIcon("执行", theme.MediaPlayIcon(), func() {
		c.runCurrent()
	})
	runBtn.Importance = widget.HighImportance
	c.runBtn = runBtn
	cancelRunBtn := widget.NewButtonWithIcon("取消", theme.MediaStopIcon(), func() {
		c.cancelRun()
	})
	cancelRunBtn.Importance = widget.HighImportance
	cancelRunBtn.Disable()
	c.cancelRunBtn = cancelRunBtn
	clearBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
		c.queryEntry.SetText("")
	})
	// Shortcut hint; kept dim so it reads as documentation, not UI.
	hint := widget.NewLabel("Ctrl+Enter 执行 · Ctrl+P 搜索表")
	hint.TextStyle = fyne.TextStyle{Italic: true}
	hint.Importance = widget.LowImportance

	limitSel := widget.NewSelect([]string{"100", "500", "1000", "5000", "无限制"}, func(s string) {
		c.limitText = s
	})
	limitSel.SetSelected("1000")

	exportBtn := widget.NewButtonWithIcon("导出", theme.DownloadIcon(), func() {
		c.exportGrid()
	})

	themeSel := widget.NewSelect([]string{"跟随系统", "浅色", "深色"}, func(s string) {
		switch s {
		case "浅色":
			c.uiSet.Theme = "light"
		case "深色":
			c.uiSet.Theme = "dark"
		default:
			c.uiSet.Theme = ""
		}
		applyTheme(c.uiSet.Theme)
	})
	themeSel.PlaceHolder = "跟随系统"
	switch c.uiSet.Theme {
	case "light":
		themeSel.SetSelected("浅色")
	case "dark":
		themeSel.SetSelected("深色")
	}

	// Toolbar order follows the reference layout: database first, the
	// prominent run button, then the shortcut hint; paging and export on the
	// right.
	toolbar := container.NewHBox(
		c.dbSel,
		runBtn, cancelRunBtn, clearBtn, hint,
		widget.NewSeparator(),
		widget.NewLabel("行数上限"),
		limitSel,
		widget.NewSeparator(),
		exportBtn,
		widget.NewSeparator(),
		themeSel,
	)

	// fyne 2.5 tabs cannot host close buttons, so new/close live on this strip.
	tabActions := container.NewHBox(
		widget.NewButtonWithIcon("新查询", theme.ContentAddIcon(), func() { c.addQueryTab() }),
		widget.NewButtonWithIcon("关闭", theme.CancelIcon(), func() { c.closeActiveTab() }),
	)
	tabsArea := container.NewBorder(tabActions, nil, nil, nil, c.qTabs)

	c.resultGrid = widget.NewTable(
		func() (int, int) { return c.gridDims() },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.TableCellID, obj fyne.CanvasObject) {
			label := obj.(*widget.Label)
			text, state := c.gridCellState(id.Row, id.Col)
			label.Truncation = fyne.TextTruncateEllipsis
			label.Alignment = fyne.TextAlignLeading
			label.TextStyle = fyne.TextStyle{}
			label.Importance = widget.MediumImportance
			switch state {
			case cellEdited:
				label.TextStyle = fyne.TextStyle{Bold: true}
				label.Importance = widget.HighImportance
			case cellDeleted:
				label.TextStyle = fyne.TextStyle{Italic: true}
				label.Importance = widget.DangerImportance
			case cellInserted:
				label.Importance = widget.HighImportance
			}
			if state == cellNormal && text == "NULL" {
				// NULLs render dim and italic so they read as empty, not as the
				// literal string "NULL".
				label.TextStyle = fyne.TextStyle{Italic: true}
				label.Importance = widget.LowImportance
			}
			// SetText refreshes the cell internally; no extra Refresh needed.
			label.SetText(text)
		},
	)
	c.resultGrid.ShowHeaderRow = true
	c.resultGrid.ShowHeaderColumn = true // left row-number column
	// Header cells are buttons so a column header can be tapped to sort.
	c.resultGrid.CreateHeader = func() fyne.CanvasObject { return widget.NewButton("", nil) }
	c.resultGrid.UpdateHeader = func(id widget.TableCellID, obj fyne.CanvasObject) {
		btn, ok := obj.(*widget.Button)
		if !ok {
			return
		}
		btn.Importance = widget.LowImportance
		btn.Alignment = widget.ButtonAlignLeading
		switch {
		case id.Col == -1: // row-number column header cells
			btn.SetText(fmt.Sprintf("%d", id.Row+1))
			btn.OnTapped = nil
		case id.Row == -1 && id.Col >= 0 && id.Col < len(c.gridHeader): // column headers
			text := c.gridHeader[id.Col]
			if id.Col == c.sortCol {
				if c.sortAsc {
					text += " ▲"
				} else {
					text += " ▼"
				}
			}
			btn.SetText(text)
			col := id.Col
			btn.OnTapped = func() { c.sortGrid(col) }
		default:
			btn.SetText("")
			btn.OnTapped = nil
		}
	}
	c.resultGrid.OnSelected = func(id widget.TableCellID) {
		c.selectedCell = id
		c.hasCell = true
		if c.cellView != nil {
			c.cellView.SetText(c.gridCell(id))
		}
		c.updateEditButtons()
	}

	c.cellView = widget.NewEntry()
	c.cellView.SetPlaceHolder("选中一个单元格查看完整值")
	c.cellView.TextStyle = fyne.TextStyle{Monospace: true}
	copyBtn := widget.NewButtonWithIcon("复制", theme.ContentCopyIcon(), func() {
		c.copyCell()
	})
	rowCopyBtn := widget.NewButtonWithIcon("复制行", theme.ContentCopyIcon(), func() {
		c.copySelectedRow()
	})
	rowCopyBtn.Importance = widget.LowImportance
	cellBar := container.NewBorder(nil, nil, widget.NewLabel("单元格"),
		container.NewHBox(copyBtn, rowCopyBtn), c.cellView)

	c.status = widget.NewLabel("就绪")

	// TablePro's signature bottom bar: status on the left, the row and change
	// actions on the right. The buttons stay disabled unless the result is a
	// writable SELECT * of one table (see setEditContext).
	c.filterBtn = widget.NewButtonWithIcon("过滤", theme.SearchIcon(), func() {
		c.showFilterDialog()
	})
	c.editCellBtn = widget.NewButtonWithIcon("编辑", theme.DocumentCreateIcon(), func() {
		c.editSelectedCell()
	})
	c.insertRowBtn = widget.NewButtonWithIcon("插行", theme.ContentAddIcon(), func() {
		c.insertRow()
	})
	c.deleteRowBtn = widget.NewButtonWithIcon("删行", theme.ContentRemoveIcon(), func() {
		c.deleteSelectedRow()
	})
	c.saveChangesBtn = widget.NewButtonWithIcon("保存", theme.ConfirmIcon(), func() {
		c.saveChanges()
	})
	c.saveChangesBtn.Importance = widget.HighImportance
	c.discardBtn = widget.NewButtonWithIcon("放弃", theme.CancelIcon(), func() {
		c.discardChanges()
	})
	for _, b := range []*widget.Button{c.filterBtn, c.editCellBtn, c.insertRowBtn,
		c.deleteRowBtn, c.saveChangesBtn, c.discardBtn} {
		b.Importance = widget.LowImportance
	}
	editActions := container.NewHBox(
		c.filterBtn, c.editCellBtn,
		c.insertRowBtn, c.deleteRowBtn,
		c.saveChangesBtn, c.discardBtn,
	)

	statusBar := container.NewBorder(nil, nil, c.status, editActions)
	bottom := container.NewVBox(cellBar, statusBar)

	// The candidate bar sits between the editor and the split handle; hidden
	// while there is nothing to suggest.
	editorArea := container.NewBorder(nil, c.sugBar, nil, nil, tabsArea)
	split := container.NewVSplit(
		editorArea,
		c.resultGrid,
	)
	split.Offset = 0.35
	if r := c.uiSet.EditorRatio; r > 0.1 && r < 0.9 {
		split.Offset = float64(r)
	}
	c.vSplit = split

	return container.NewBorder(toolbar, bottom, nil, nil, split)
}

// limitValue parses the chosen row limit; "无限制" means no cap (0).
func (c *Client) limitValue() int {
	if c.limitText == "无限制" {
		return 0
	}
	if n, err := strconv.Atoi(c.limitText); err == nil && n > 0 {
		return n
	}
	return 1000
}

// dbSelRefresh repopulates the database selector for the live connection.
func (c *Client) dbSelRefresh() {
	if c.dbSel == nil {
		return
	}
	if c.currentID == "" || !c.store.ConnectionStatus(c.currentID) {
		c.dbSel.Disable()
		c.dbSel.SetOptions(nil)
		return
	}
	dbs, err := c.store.Databases(c.currentID)
	if err != nil {
		c.dbSel.Enable()
		c.setStatus("数据库列表加载失败：" + err.Error())
		return
	}
	if len(dbs) == 0 {
		c.dbSel.Enable()
		return
	}
	c.mu.Lock()
	c.dbList = dbs
	c.mu.Unlock()
	c.dbSel.Enable()
	c.dbSel.SetOptions(dbs)
	if c.currentDB == "" {
		c.currentDB = dbs[0]
	}
	c.dbSel.SetSelected(c.resolveDB())
}

// resolveDB returns the effective database to run against.
func (c *Client) resolveDB() string {
	if c.currentDB != "" {
		return c.currentDB
	}
	if cfg, ok := c.store.LiveConfig(c.currentID); ok {
		return cfg.Database
	}
	return ""
}

// runCurrent evaluates the editor text: a single statement runs directly, a
// multi-statement script runs in order and stops at the first failure, with
// every statement recorded in the history. The query executes on a background
// goroutine so the window stays responsive; a cancel button aborts it, and
// the result is marshalled back to the UI thread before the grid updates.
func (c *Client) runCurrent() {
	if c.currentID == "" {
		dialog.ShowInformation("未连接", "请先在左侧选择一个连接并点击「连接」。", c.win)
		return
	}
	if !c.store.ConnectionStatus(c.currentID) {
		dialog.ShowInformation("未连接", "该连接尚未建立，请先连接。", c.win)
		return
	}
	q := c.queryEntry.Text
	if strings.TrimSpace(q) == "" {
		return
	}
	stmts := db.SplitStatements(q)
	if len(stmts) == 0 {
		c.setStatus("没有可执行的语句（只有注释或空白）")
		return
	}
	conn, err := c.store.OpenConn(c.currentID)
	if err != nil {
		c.setStatus("错误：" + err.Error())
		return
	}
	dbName := c.resolveDB()
	limit := c.limitValue()

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancelFn = cancel
	c.mu.Unlock()
	c.setRunning(true)
	c.setStatus("执行中 ...（点「取消」可中断）")

	// The whole query + history-record runs off the UI thread.
	work := func() (db.QueryResult, string) {
		if len(stmts) == 1 {
			res := c.store.Execute(ctx, conn, dbName, stmts[0], limit)
			c.store.RecordStatement(c.currentID, c.currentDB, stmts[0], res)
			return res, stmts[0]
		}
		last, lastStmt, _ := c.store.RunScript(ctx, conn, dbName, stmts, limit)
		return last, lastStmt
	}
	finish := func(res db.QueryResult, lastStmt string) {
		c.mu.Lock()
		c.cancelFn = nil
		c.mu.Unlock()
		c.setRunning(false)
		c.setEditContext(lastStmt, res)
		c.renderResult(res)
		c.refreshHistory()
		c.clearTabDirty(c.queryEntry)
		if ctx.Err() == context.Canceled && res.Error != "" {
			c.setStatus("查询已取消")
		}
		c.focusEditor()
	}

	if c.asyncRun {
		go func() {
			res, lastStmt := work()
			cancel()
			c.dispatch(func() { finish(res, lastStmt) })
		}()
	} else {
		res, lastStmt := work()
		cancel()
		finish(res, lastStmt)
	}
}

// cancelRun aborts the in-flight query, if any.
func (c *Client) cancelRun() {
	c.mu.Lock()
	fn := c.cancelFn
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// setRunning toggles the run/cancel buttons for an in-flight query.
func (c *Client) setRunning(running bool) {
	if c.runBtn != nil {
		if running {
			c.runBtn.Disable()
		} else {
			c.runBtn.Enable()
		}
	}
	if c.cancelRunBtn != nil {
		if running {
			c.cancelRunBtn.Enable()
		} else {
			c.cancelRunBtn.Disable()
		}
	}
}
