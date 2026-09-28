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

// buildQueryView assembles the editor/grid half of the main window. Each region
// is built by its own assembler (toolbar, editor area, result grid, cell bar,
// status bar); this function only wires them into the border/split, so the
// layout - and the layout tests that pin it - stays readable.
func (c *Client) buildQueryView() fyne.CanvasObject {
	toolbar := c.buildQueryToolbar()
	editorArea := c.buildEditorArea()
	c.buildResultGrid()
	bottom := container.NewVBox(c.buildCellBar(), c.buildStatusBar())

	// The page bar rides above the grid and is hidden unless the result on
	// screen is a paged one, so a normal query costs no vertical space.
	gridArea := container.NewBorder(c.buildPageBar(), nil, nil, nil, c.grid.table)
	split := container.NewVSplit(
		editorArea,
		gridArea,
	)
	split.Offset = 0.35
	if r := c.uiSet.EditorRatio; r > 0.1 && r < 0.9 {
		split.Offset = float64(r)
	}
	c.vSplit = split

	return container.NewBorder(toolbar, bottom, nil, nil, split)
}

// buildEditorArea creates the query tabs plus the strip that hosts new/close
// and script open/save. Everything downstream addresses the active editor
// through c.tabs.entry, so switching tabs is just re-pointing that alias.
func (c *Client) buildEditorArea() fyne.CanvasObject {
	c.tabs.sug = container.NewVBox()
	c.tabs.sug.Hide()
	c.tabs.seq = 0
	c.tabs.bar = container.NewAppTabs()
	c.tabs.bar.OnSelected = func(item *container.TabItem) { c.syncActiveTab(item) }
	c.addQueryTab()
	c.restoreQueryTabs()

	openScriptBtn := widget.NewButtonWithIcon("打开脚本", theme.FolderOpenIcon(), func() {
		c.showOpenScriptDialog()
	})
	openScriptBtn.Importance = widget.LowImportance
	saveScriptBtn := widget.NewButtonWithIcon("保存脚本", theme.DocumentSaveIcon(), func() {
		c.showSaveScriptDialog()
	})
	saveScriptBtn.Importance = widget.LowImportance

	// fyne 2.5 tabs cannot host close buttons, so new/close live on this strip,
	// and script open/save sit with them because both act on the active tab.
	tabActions := container.NewHBox(
		widget.NewButtonWithIcon("新查询", theme.ContentAddIcon(), func() { c.addQueryTab() }),
		widget.NewButtonWithIcon("关闭", theme.CancelIcon(), func() { c.closeActiveTab() }),
		widget.NewSeparator(),
		openScriptBtn, saveScriptBtn,
	)
	tabsArea := container.NewBorder(tabActions, nil, nil, nil, c.tabs.bar)

	// The candidate bar sits between the editor and the split handle; hidden
	// while there is nothing to suggest.
	return container.NewBorder(nil, c.tabs.sug, nil, nil, tabsArea)
}

// buildQueryToolbar builds the strip above the tabs. Its order follows the
// reference layout: database first, the prominent run button, then the shortcut
// dialog; paging and export on the right.
func (c *Client) buildQueryToolbar() *fyne.Container {
	c.dbs.sel = widget.NewSelect(nil, func(db string) {
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
		c.tabs.entry.SetText("")
	})
	// The shortcut hint used to be a 300px label here, which alone pushed the
	// toolbar past the width of the main pane and clipped 导出/主题. It is now a
	// button over the dialog that bindKey documents (see ui_keys.go).
	keysBtn := widget.NewButtonWithIcon("快捷键", theme.HelpIcon(), c.showKeysDialog)
	keysBtn.Importance = widget.LowImportance

	limitSel := widget.NewSelect([]string{"100", "500", "1000", "5000", "无限制"}, func(s string) {
		c.page.limitText = s
	})
	limitSel.SetSelected("1000")
	c.page.limitSel = limitSel

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

	c.page.check = c.buildPagingCheck()
	return container.NewHBox(
		c.dbs.sel,
		runBtn, cancelRunBtn, clearBtn, keysBtn,
		widget.NewSeparator(),
		widget.NewLabel("行数上限"),
		limitSel, c.page.check,
		widget.NewSeparator(),
		exportBtn,
		widget.NewSeparator(),
		themeSel,
	)
}

// buildResultGrid builds the shared result table: header cells are buttons so a
// column header can be tapped to sort, and each cell's style comes from
// gridCellState (edits, deletes, NULLs, search hits).
func (c *Client) buildResultGrid() {
	c.grid.table = widget.NewTable(
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
			if state == cellNormal && c.isSearchHit(id.Row, id.Col) {
				label.TextStyle = fyne.TextStyle{Bold: true}
				label.Importance = widget.HighImportance
			}
			// SetText refreshes the cell internally; no extra Refresh needed.
			label.SetText(text)
		},
	)
	c.grid.table.ShowHeaderRow = true
	c.grid.table.ShowHeaderColumn = true // left row-number column
	c.grid.table.CreateHeader = func() fyne.CanvasObject { return widget.NewButton("", nil) }
	c.grid.table.UpdateHeader = func(id widget.TableCellID, obj fyne.CanvasObject) {
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
		case id.Row == -1 && id.Col >= 0 && id.Col < len(c.grid.header): // column headers
			text := c.grid.header[id.Col]
			if id.Col == c.grid.sortCol {
				if c.grid.sortAsc {
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
	c.grid.table.OnSelected = func(id widget.TableCellID) {
		c.grid.cell = id
		c.grid.hasCell = true
		if c.grid.view != nil {
			c.grid.view.SetText(c.gridCell(id))
		}
		c.updateEditButtons()
	}
}

// buildCellBar builds the row under the grid: the full value of the selected
// cell on the left, the in-result search and the copy actions on the right.
func (c *Client) buildCellBar() fyne.CanvasObject {
	c.grid.view = newKeyedEntry()
	c.grid.view.SetPlaceHolder("选中一个单元格查看完整值")
	c.grid.view.TextStyle = fyne.TextStyle{Monospace: true}
	c.grid.view.onShortcut = c.entryShortcut
	c.find.entry = newSearchEntry()
	c.find.entry.SetPlaceHolder("在结果中搜索")
	c.find.entry.OnChanged = c.onGridSearchChanged
	c.find.entry.onShortcut = c.entryShortcut
	c.find.entry.onNext = func() { c.stepSearch(1) }
	c.find.entry.onPrev = func() { c.stepSearch(-1) }
	c.find.label = widget.NewLabel("")
	c.find.label.Importance = widget.LowImportance
	copyBtn := widget.NewButtonWithIcon("复制", theme.ContentCopyIcon(), func() {
		c.copyCell()
	})
	rowCopyBtn := widget.NewButtonWithIcon("复制行", theme.ContentCopyIcon(), func() {
		c.copySelectedRow()
	})
	rowCopyBtn.Importance = widget.LowImportance
	return container.NewBorder(
		nil,
		nil,
		widget.NewLabel("单元格"),
		container.NewHBox(c.find.entry, c.find.label, copyBtn, rowCopyBtn),
		c.grid.view,
	)
}

// buildStatusBar builds TablePro's signature bottom bar: status on the left,
// the row and change actions on the right. The buttons stay disabled unless the
// result is a writable SELECT * of one table (see setEditContext).
func (c *Client) buildStatusBar() fyne.CanvasObject {
	c.status = widget.NewLabel("就绪")
	c.act.filter = widget.NewButtonWithIcon("过滤", theme.SearchIcon(), func() {
		c.showFilterDialog()
	})
	c.act.editCell = widget.NewButtonWithIcon("编辑", theme.DocumentCreateIcon(), func() {
		c.editSelectedCell()
	})
	c.act.insertRow = widget.NewButtonWithIcon("插行", theme.ContentAddIcon(), func() {
		c.insertRow()
	})
	c.act.deleteRow = widget.NewButtonWithIcon("删行", theme.ContentRemoveIcon(), func() {
		c.deleteSelectedRow()
	})
	c.act.saveChanges = widget.NewButtonWithIcon("保存", theme.ConfirmIcon(), func() {
		c.saveChanges()
	})
	c.act.saveChanges.Importance = widget.HighImportance
	c.act.discard = widget.NewButtonWithIcon("放弃", theme.CancelIcon(), func() {
		c.discardChanges()
	})
	for _, b := range []*widget.Button{c.act.filter, c.act.editCell, c.act.insertRow,
		c.act.deleteRow, c.act.saveChanges, c.act.discard} {
		b.Importance = widget.LowImportance
	}
	editActions := container.NewHBox(
		c.act.filter, c.act.editCell,
		c.act.insertRow, c.act.deleteRow,
		c.act.saveChanges, c.act.discard,
	)
	return container.NewBorder(nil, nil, c.status, editActions)
}

// limitValue parses the chosen row limit; "无限制" means no cap (0).
func (c *Client) limitValue() int {
	if c.page.limitText == "无限制" {
		return 0
	}
	if n, err := strconv.Atoi(c.page.limitText); err == nil && n > 0 {
		return n
	}
	return 1000
}

// dbSelRefresh repopulates the database selector for the live connection.
func (c *Client) dbSelRefresh() {
	if c.dbs.sel == nil {
		return
	}
	if c.currentID == "" || !c.store.ConnectionStatus(c.currentID) {
		c.dbs.sel.Disable()
		c.dbs.sel.SetOptions(nil)
		return
	}
	dbs, err := c.store.Databases(c.currentID)
	if err != nil {
		c.dbs.sel.Enable()
		c.setStatus("数据库列表加载失败：" + err.Error())
		return
	}
	if len(dbs) == 0 {
		c.dbs.sel.Enable()
		return
	}
	c.mu.Lock()
	c.dbs.list = dbs
	c.mu.Unlock()
	c.dbs.sel.Enable()
	c.dbs.sel.SetOptions(dbs)
	if c.currentDB == "" {
		c.currentDB = dbs[0]
	}
	c.dbs.sel.SetSelected(c.resolveDB())
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

// querySession snapshots, under the session lock, the state one background
// query round runs against. currentID/currentDB/pageSize belong to the UI
// thread - connect and disconnect rewrite the first two while a query is in
// flight, and the 每页 select rewrites the third - so a work closure must never
// reach for them; it carries this copy instead. The database is the effective
// one, which is also what the history entry must be filed under.
func (c *Client) querySession() (connID, database string, pageSize int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentID, c.resolveDB(), c.page.size
}

// runCurrent evaluates the editor text: a single statement runs directly, a
// multi-statement script runs in order and stops at the first failure, with
// every statement recorded in the history. While 分页浏览 is on and the editor
// holds one pageable SELECT, that statement runs as page 1 of the chosen row
// window instead of the 行数上限-capped whole result. The query executes on a
// background goroutine so the window stays responsive; a cancel button aborts
// it, and the result is marshalled back to the UI thread before the grid
// updates.
func (c *Client) runCurrent() {
	if c.currentID == "" {
		dialog.ShowInformation("未连接", "请先在左侧选择一个连接并点击「连接」。", c.win)
		return
	}
	if !c.store.ConnectionStatus(c.currentID) {
		dialog.ShowInformation("未连接", "该连接尚未建立，请先连接。", c.win)
		return
	}
	q := c.tabs.entry.Text
	if strings.TrimSpace(q) == "" {
		return
	}
	stmts := db.SplitStatements(q)
	if len(stmts) == 0 {
		c.setStatus("没有可执行的语句（只有注释或空白）")
		return
	}
	connID, dbName, pageSize := c.querySession()
	conn, err := c.store.OpenConn(connID)
	if err != nil {
		c.setStatus("错误：" + err.Error())
		return
	}
	limit := c.limitValue()
	// Paging covers one statement only; a script's last result is whatever the
	// script ended with, which is not something a page bar can re-run honestly.
	paged := c.page.on && len(stmts) == 1 && db.Pageable(conn.Config.Type, stmts[0])
	var pageNote string

	c.runTracked("执行中 ...（点「取消」可中断）",
		func(ctx context.Context) (db.QueryResult, string) {
			// The whole query + history-record runs off the UI thread.
			if paged {
				// Making the window stable reads the catalog, so it belongs
				// here rather than on the UI thread.
				stmt, note := c.stablePageQuery(conn, dbName, stmts[0])
				pageNote = note
				res := c.store.ExecutePage(ctx, conn, dbName, stmt, 1, pageSize)
				c.store.RecordStatement(connID, dbName, stmts[0], res)
				return res, stmt
			}
			if len(stmts) == 1 {
				res := c.store.Execute(ctx, conn, dbName, stmts[0], limit)
				c.store.RecordStatement(connID, dbName, stmts[0], res)
				return res, stmts[0]
			}
			last, lastStmt, _ := c.store.RunScript(ctx, conn, dbName, stmts, limit)
			return last, lastStmt
		},
		func(res db.QueryResult, lastStmt string, canceled bool) {
			c.mu.Lock()
			c.cancelFn = nil
			c.mu.Unlock()
			c.setRunning(false)
			c.setEditContext(lastStmt, res)
			c.renderResult(res)
			c.afterRunPaging(lastStmt, res, pageNote)
			c.refreshHistory()
			c.clearTabDirty(c.tabs.entry)
			if canceled && res.Error != "" {
				c.setStatus("查询已取消")
			}
			c.focusEditor()
		})
}

// runTracked runs one query round with the 取消 button wired to it: work runs on
// a background goroutine (inline when asyncRun is false, for deterministic
// tests) and finish receives its result on the UI thread. runCurrent and
// runPage share the cancellation, busy-button and marshalling bookkeeping.
func (c *Client) runTracked(busy string, work func(context.Context) (db.QueryResult, string),
	finish func(db.QueryResult, string, bool)) {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancelFn = cancel
	c.mu.Unlock()
	c.setRunning(true)
	c.setStatus(busy)

	run := func() (db.QueryResult, string, bool) {
		res, lastStmt := work(ctx)
		// Read the cancellation flag before cancel() below: a cancelled context
		// reports Canceled for every query afterwards, which would make even a
		// plain error look like a user cancellation.
		canceled := ctx.Err() == context.Canceled
		cancel()
		return res, lastStmt, canceled
	}
	if !c.asyncRun {
		res, lastStmt, canceled := run()
		finish(res, lastStmt, canceled)
		return
	}
	go func() {
		res, lastStmt, canceled := run()
		c.dispatch(func() { finish(res, lastStmt, canceled) })
	}()
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
