package main

import (
	"context"
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
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
	// 工具条底部压一道极淡阴影（tyde/Material 的悬浮 App Bar），从编辑器/网格区托起。
	// 必须包在放进 Border 之前：buildQueryView 会把 toolBar 作为顶边写进 Border，
	// 之后在 buildUI 再包一层会脱离布局树（尺寸变成 0，布局测试会挂）。
	// 阴影是 2px 主题矩形，不动工具条自身 MinSize，TestToolbarFitsMainPane 仍通过。
	toolbar = c.elevatedEdge(edgeBottom, toolbar)
	c.queryToolbar = toolbar
	editorArea := c.buildEditorArea()
	c.buildResultGrid()
	bottom := c.buildBottomBar()

	// The grid sits on its own white rectangle. widget.Table does not paint a
	// background of its own, so the window's grey would otherwise show through
	// and the lower half of the window would read as a different surface from
	// the editor above it.
	c.gridBG = canvas.NewRectangle(contentBG())
	c.themedRects = append(c.themedRects, themedRect{rect: c.gridBG, fill: contentBG})

	// The breadcrumb rides above the grid with the page bar directly under it;
	// the page bar stays hidden unless the result on screen is a paged one, so
	// a normal query costs no vertical space beyond the crumb row. The data bar
	// - the TablePlus signature strip: the object name on the left, the row
	// actions (add / duplicate / delete / reload) on the right - sits just over
	// the grid. The bottom bar keeps the cell inspector and edit-commit cluster.
	gridArea := container.NewBorder(
		container.NewVBox(c.buildCrumbBar(), c.buildPageBar(), c.buildDataBar()),
		nil, nil, nil, container.NewStack(c.gridBG, c.grid.table))
	split := container.NewVSplit(
		editorArea,
		gridArea,
	)
	split.Offset = 0.35
	if r := c.uiSet.EditorRatio; r > 0.1 && r < 0.9 {
		split.Offset = float64(r)
	}
	c.vSplit = split

	// 右侧详情栏默认隐藏，由面包屑的「详情」按钮开（见 ui_details.go）。
	return container.NewBorder(toolbar, bottom, nil, c.buildDetailsPane(), split)
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

	// fyne's tabs cannot host their own close button, so new/close and the two
	// script buttons sit on the tab strip's right - which is where TablePlus
	// puts its "+" - instead of on a row of their own above it. The row they
	// used to cost goes back to the editor.
	//
	// The HBox has to go inside a VBox: a Border's right slot is stretched to
	// the container's full height and an HBox stretches its children to match,
	// which would turn four small buttons into one tall block. A VBox hands
	// them their own heights and aligns them to the top.
	c.tabs.actions = container.NewHBox(
		attachTooltip(iconButton(theme.ContentAddIcon(), func() { c.addQueryTab() }), "新建查询标签"),
		attachTooltip(iconButton(theme.CancelIcon(), func() { c.closeActiveTab() }), "关闭当前标签"),
		attachTooltip(iconButton(theme.FolderOpenIcon(), func() { c.showOpenScriptDialog() }), "打开 .sql 脚本到当前标签"),
		attachTooltip(iconButton(theme.DocumentSaveIcon(), func() { c.showSaveScriptDialog() }), "把当前标签保存为 .sql 脚本"),
	)
	tabsArea := container.NewBorder(nil, nil, nil,
		container.NewVBox(c.tabs.actions), c.tabs.bar)

	// The candidate bar sits between the editor and the split handle; hidden
	// while there is nothing to suggest.
	return container.NewBorder(nil, c.tabs.sug, nil, nil, tabsArea)
}

// iconButton is a toolbar-shaped button: no label, the size of its icon. The
// ones that lose their text still have to be findable, so every caller hangs a
// tooltip on it (see ui_tooltip.go).
func iconButton(icon fyne.Resource, fn func()) *widget.Button {
	b := widget.NewButtonWithIcon("", icon, fn)
	b.Importance = widget.LowImportance
	return b
}

// buildQueryToolbar builds the strip above the tabs, in three groups like
// TablePlus's: the database on the left, the run controls in the middle and the
// result controls pinned to the right edge.
//
// The groups are a Border rather than one HBox because an HBox never wraps: it
// lays every child at its own width and pushes the tail past the right edge,
// where it is clipped and unreachable. A Border also gives the tail a home - it
// is pinned to the right edge instead of drifting further left with every
// control added to its left, so the right-hand group stays where the eye
// expects it at any window width.
func (c *Client) buildQueryToolbar() *fyne.Container {
	c.dbs.sel = widget.NewSelect(nil, func(db string) {
		c.currentDB = db
		c.expandDatabaseInTree(db)
	})
	c.dbs.sel.PlaceHolder = "数据库"

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
	// 图标按钮没有文字，工具栏上不可发现，每个都挂一个悬停提示
	// （见 ui_tooltip.go）。快捷键那条尤甚：它取代的是一条 300px 的说明标签，
	// 那条标签自己就把工具条顶出了主区宽度、把导出与主题裁掉了。
	clearBtn := attachTooltip(iconButton(theme.DeleteIcon(), func() {
		c.tabs.entry.SetText("")
	}), "清空编辑器")
	keysBtn := attachTooltip(iconButton(theme.HelpIcon(), c.showKeysDialog),
		"快捷键一览（Ctrl/Cmd+P、N、Enter…）")
	// 格式化保留文字：fyne 2.8 的图标表里没有「格式化 / 美化」语义的图标，
	// MenuIcon 与 HistoryIcon 都会被误读成别的东西，不如直接写字。
	fmtBtn := widget.NewButtonWithIcon("格式化", theme.MenuIcon(), c.formatCurrentSQL)
	fmtBtn.Importance = widget.LowImportance

	limitSel := widget.NewSelect(rowLimitOptions, func(s string) {
		c.page.limitText = s
		c.uiSet.RowLimit = s
	})
	if validRowLimit(c.uiSet.RowLimit) {
		limitSel.SetSelected(c.uiSet.RowLimit)
	} else {
		limitSel.SetSelected("1000")
	}
	c.page.limitText = limitSel.Selected
	c.page.limitSel = limitSel
	// 行数上限的说明从一行文字标签挪到下拉自己的提示上：标签占了 ~70px，
	// 窄窗口下这 70px 比标签本身值钱。
	attachTooltip(limitSel, "行数上限（语句自带行窗口时不生效）")

	exportBtn := attachTooltip(iconButton(theme.DownloadIcon(), func() {
		c.exportGrid()
	}), "导出当前结果（CSV / JSON / INSERT / Excel）")

	themeSel := widget.NewSelect([]string{"系统", "浅色", "深色"}, func(s string) {
		switch s {
		case "浅色":
			c.uiSet.Theme = "light"
		case "深色":
			c.uiSet.Theme = "dark"
		default:
			c.uiSet.Theme = ""
		}
		applyTheme(c.uiSet.Theme)
		// The shapes that read a colour out of the old theme keep it: they have
		// to be repainted, or switching to dark leaves a white rectangle under
		// the grid.
		c.refreshThemeColors()
	})
	themeSel.PlaceHolder = "主题"
	switch c.uiSet.Theme {
	case "light":
		themeSel.SetSelected("浅色")
	case "dark":
		themeSel.SetSelected("深色")
	}

	c.page.check = c.buildPagingCheck()
	if c.uiSet.PagingOn {
		// The switch itself restores its look; the callback's side effects
		// (greyed-out limit, reset page state) are replayed by hand, because
		// SetChecked does not fire the toggle.
		c.page.on = true
		c.page.stmt = ""
		c.page.note = ""
		c.page.no = 1
		c.page.check.SetChecked(true)
		limitSel.Disable()
	}
	centre := container.NewHBox(
		runBtn, cancelRunBtn,
		widget.NewSeparator(),
		clearBtn, keysBtn, fmtBtn,
	)
	right := container.NewHBox(
		limitSel, c.page.check,
		widget.NewSeparator(),
		exportBtn, themeSel,
	)
	c.queryToolbar = container.NewBorder(nil, nil, c.dbs.sel, right, centre)
	return c.queryToolbar
}

// buildResultGrid builds the shared result table: header cells are buttons so a
// column header can be tapped to sort, and each cell's style comes from
// gridCellState (edits, deletes, NULLs, search hits). Cells are a translucent
// zebra rectangle under the label - see zebraColor for why it must stay
// translucent - and numeric columns align to the trailing edge like TablePlus.
func (c *Client) buildResultGrid() {
	c.grid.table = widget.NewTable(
		func() (int, int) { return c.gridDims() },
		func() fyne.CanvasObject {
			stripe := canvas.NewRectangle(color.Transparent)
			stripe.CornerRadius = 0
			return container.NewStack(stripe, widget.NewLabel(""))
		},
		func(id widget.TableCellID, obj fyne.CanvasObject) {
			pair, ok := obj.(*fyne.Container)
			if !ok || len(pair.Objects) != 2 {
				return
			}
			stripe, _ := pair.Objects[0].(*canvas.Rectangle)
			label, _ := pair.Objects[1].(*widget.Label)
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
			if state == cellNormal && c.cellIsNull(id.Row, id.Col) {
				// NULLs render dim and italic so they read as empty, not as the
				// literal string "NULL".
				label.TextStyle = fyne.TextStyle{Italic: true}
				label.Importance = widget.LowImportance
			}
			if state == cellNormal && c.isSearchHit(id.Row, id.Col) {
				label.TextStyle = fyne.TextStyle{Bold: true}
				label.Importance = widget.HighImportance
			}
			if c.colAlignsRight(id.Col) {
				label.Alignment = fyne.TextAlignTrailing
			}
			// Zebra stripes only on data cells: header and the row-number column
			// keep their own plain look.
			if stripe != nil && id.Col >= 0 && id.Row%2 == 1 && state != cellEdited &&
				state != cellDeleted && state != cellInserted {
				stripe.FillColor = zebraColor()
			} else if stripe != nil {
				stripe.FillColor = color.Transparent
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
		c.refreshDetails() // 右侧详情栏跟随选中行
	}
}

// buildDataBar builds the TablePlus-style strip directly above the result grid:
// the object name on the left and the row actions on the right. The actions are
// disabled until a writable result is on screen (see setEditContext); reload
// re-runs the active tab.
func (c *Client) buildDataBar() fyne.CanvasObject {
	c.dataName = widget.NewLabel("")
	c.dataName.Importance = widget.MediumImportance
	c.dataName.Truncation = fyne.TextTruncateEllipsis
	addRaw := iconButton(theme.ContentAddIcon(), func() { c.insertRow() })
	copyRaw := iconButton(theme.ContentCopyIcon(), func() { c.copySelectedRow() })
	delRaw := iconButton(theme.ContentRemoveIcon(), func() { c.deleteSelectedRow() })
	reloadRaw := iconButton(theme.ViewRefreshIcon(), func() { c.reloadActive() })
	addBtn := attachTooltip(addRaw, "新增一行")
	copyBtn := attachTooltip(copyRaw, "复制选中行")
	delBtn := attachTooltip(delRaw, "删除选中行")
	reloadBtn := attachTooltip(reloadRaw, "重新执行查询 / 刷新结果")
	c.dbar.insertRow, c.dbar.copyRow, c.dbar.deleteRow, c.dbar.reload = addRaw, copyRaw, delRaw, reloadRaw
	for _, b := range []*widget.Button{addRaw, copyRaw, delRaw} {
		b.Disable()
	}
	right := container.NewHBox(addBtn, copyBtn, delBtn, reloadBtn)
	return container.NewBorder(nil, nil, c.dataName, right, nil)
}

// reloadActive re-runs the active query tab, which is what 刷新 means on a data
// view: the grid is a snapshot, so the only way back to fresh data is to
// execute the statement again.
func (c *Client) reloadActive() {
	c.runAllStatements()
}

// buildBottomBar is the single bottom strip. It folds the two rows this used to
// be — 单元格 row on top, status row below — into one, matching the single
// bottom bar the embedded copy shipped for the TablePlus layout.
//
// Left is the status, centre carries the selected cell's full value plus the
// in-result search and copy actions, right holds the row/change actions. Those
// stay disabled unless the result is a writable SELECT * of one table (see
// setEditContext).
//
// Two things the embedded copy's bar had are deliberately NOT duplicated: the
// range text (独立版 already shows it in the page bar over the grid — see
// ui_paging.go) and the 数据/结构/索引 segment (独立版 opens 结构 as a dialog
// from the schema tree; an inline segment waits for the P3 index pane).
func (c *Client) buildBottomBar() fyne.CanvasObject {
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
	// 复制为 keeps the export formats but skips the file dialog: the grid goes
	// straight to the clipboard, which is where it is usually wanted next.
	copyAsBtn := widget.NewButtonWithIcon("复制为", theme.ContentCopyIcon(), nil)
	copyAsBtn.Importance = widget.LowImportance
	copyAsBtn.OnTapped = func() { c.showCopyAsMenu(copyAsBtn) }

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

	centre := container.NewHBox(
		widget.NewLabel("单元格"), c.grid.view,
		c.find.entry, c.find.label,
		copyBtn, rowCopyBtn, copyAsBtn,
	)
	return container.NewBorder(nil, nil, c.status, editActions, centre)
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

// dbSelRefresh repopulates the database selector for the live connection. That
// list is a catalog query, so it runs off the event thread and the selector is
// filled in the callback - the toolbar used to stop drawing while a remote
// server answered it.
func (c *Client) dbSelRefresh() {
	if c.dbs.sel == nil {
		return
	}
	if c.currentID == "" || !c.store.ConnectionStatus(c.currentID) {
		c.dbs.sel.Disable()
		c.dbs.sel.SetOptions(nil)
		return
	}
	id := c.currentID
	var (
		dbs   []string
		dbErr error
	)
	c.background(func(ctx context.Context) {
		dbs, dbErr = c.store.Databases(ctx, id)
	}, func() {
		if dbErr != nil {
			c.dbs.sel.Enable()
			c.setStatusError("数据库列表加载失败：" + dbErr.Error())
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
	})
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

// runScope says what Ctrl+Enter means once the editor can hold more than one
// statement: the one the caret sits in (or the selection), or the whole tab.
type runScope int

const (
	scopeCaret runScope = iota
	scopeAll
)

// runPlan is one resolved request to execute something: the statements
// themselves, how the status bar describes them while they run and what it
// adds to the result line afterwards, and whether this was the whole tab -
// the ● marker means "changed since you last ran IT", so only running the
// whole tab clears it.
type runPlan struct {
	stmts []string
	busy  string // "" keeps the plain 执行中 wording this has always used
	note  string // appended to the result line, "" for nothing to add
	whole bool
}

// runCurrent executes what the caret asks for: with text selected, that
// selection; otherwise the statement the caret is in. Running the whole tab
// moved to Ctrl+Shift+Enter (see runAllStatements).
func (c *Client) runCurrent() { c.runWithScope(scopeCaret) }

// runAllStatements executes every statement in the tab, which is what
// Ctrl+Enter used to do unconditionally. The in-app re-reads after a write
// (saving grid changes, applying a column filter) call it deliberately: they
// re-run whatever produced the grid on screen, and must keep doing exactly
// that rather than whatever sits under the caret today.
func (c *Client) runAllStatements() { c.runWithScope(scopeAll) }

// runWithScope checks the session and the editor, resolves the scope into the
// statements to run, and hands them over. A destructive statement is confirmed
// once here, in front of the whole plan: a script is one action as far as the
// user is concerned, so one dangerous statement in it is enough to describe
// the run.
func (c *Client) runWithScope(scope runScope) {
	if c.currentID == "" {
		dialog.ShowInformation("未连接", "请先在左侧选择一个连接并点击「连接」。", c.win)
		return
	}
	if !c.store.ConnectionStatus(c.currentID) {
		dialog.ShowInformation("未连接", "该连接尚未建立，请先连接。", c.win)
		return
	}
	if strings.TrimSpace(c.tabs.entry.Text) == "" {
		return
	}
	plan, ok := c.planForScope(scope)
	if !ok {
		c.setStatusWarn("没有可执行的语句（光标在只有注释或空白的地方）")
		return
	}
	if warn := c.destructiveWarning(plan); warn != "" {
		dialog.ShowConfirm("确认执行破坏性语句", warn, func(confirmed bool) {
			if !confirmed {
				c.setStatusWarn("已取消：破坏性语句没有执行")
				return
			}
			c.confirmPendingEdits(plan)
		}, c.win)
		return
	}
	c.confirmPendingEdits(plan)
}

// destructiveWarning describes why a plan is destructive, or returns "" when
// it is not (or when it cannot tell - see db.DestructiveRisk). The statement
// goes in the dialog so the user is confirming text they can read, not a
// summary somebody else wrote.
func (c *Client) destructiveWarning(plan runPlan) string {
	var first, reason, kinds string
	n := 0
	for _, s := range plan.stmts {
		kind, why := db.DestructiveRisk(s)
		if kind == db.RiskNone {
			continue
		}
		n++
		if n == 1 {
			first, reason, kinds = s, why, string(kind)
		} else {
			kinds += " / " + string(kind)
		}
	}
	if n == 0 {
		return ""
	}
	head := reason
	if n > 1 {
		head = fmt.Sprintf("这批语句里有 %d 条是破坏性的（%s）：%s", n, kinds, reason)
	}
	return head + "\n\n" + first
}

// confirmPendingEdits runs the plan, asking first if the grid holds changes
// that a new result would silently drop.
func (c *Client) confirmPendingEdits(plan runPlan) {
	// A new result replaces the grid and setEditContext resets the edit queue
	// along with it, so unsaved cell changes would vanish without a trace.
	// Paging and sorting already refuse to move while changes are pending;
	// execution is the one path that could still drop them, so it asks first.
	if c.grid.edits.any() {
		dialog.ShowConfirm("确认执行",
			"结果表上有未保存的更改，执行新查询会把它们丢弃。仍要执行吗？",
			func(confirmed bool) {
				if confirmed {
					c.runStatements(plan)
				}
			}, c.win)
		return
	}
	c.runStatements(plan)
}

// planForScope turns the editor's text into what this keystroke executes.
//
// Why the statement, and not its position in the text, decides: the caret can
// sit in the middle of a string or a comment, so "which statement" cannot be
// answered by looking for the nearest semicolon - db.StmtAt reads statements
// the way the splitter does and answers with the one whose span encloses the
// caret. For a selection there is no span to ask about either, because fyne
// hands over the selected TEXT but not where it starts; selectionRange recovers
// the position from the caret and repeated text can make that ambiguous, so a
// selection whose position cannot be recovered still runs - just without being
// able to snap to statement bounds.
func (c *Client) planForScope(scope runScope) (runPlan, bool) {
	e := c.tabs.entry
	text := e.Text
	all := db.SplitStatementsSpans(text)
	if len(all) == 0 {
		return runPlan{}, false
	}
	if scope == scopeAll {
		return runPlan{stmts: spanTexts(all), whole: true}, true
	}
	caret := caretOffset(text, e.CursorRow, e.CursorColumn)
	if sel := e.SelectedText(); strings.TrimSpace(sel) != "" {
		if lo, hi, ok := selectionRange(text, sel, caret); ok {
			if picked := db.StmtsInRange(text, lo, hi); len(picked) > 0 {
				return runPlan{stmts: spanTexts(picked), note: selectionNote(len(picked), len(all)),
					busy: fmt.Sprintf("执行选区中的 %d 条语句", len(picked))}, true
			}
		}
		if picked := db.SplitStatements(sel); len(picked) > 0 {
			return runPlan{stmts: picked, note: selectionNote(len(picked), len(all)),
				busy: fmt.Sprintf("执行选区中的 %d 条语句", len(picked))}, true
		}
		return runPlan{}, false
	}
	if caret < 0 {
		caret = 0
	}
	stmt, ok := db.StmtAt(text, caret)
	if !ok {
		return runPlan{}, false
	}
	i := spanIndex(all, stmt.Start)
	if len(all) == 1 {
		// One statement in the tab: no need to say which of them this was.
		return runPlan{stmts: []string{stmt.Text}, whole: true}, true
	}
	return runPlan{
		stmts: []string{stmt.Text},
		busy:  fmt.Sprintf("执行第 %d/%d 条语句", i+1, len(all)),
		note:  fmt.Sprintf(" · 编辑器共 %d 条语句，这次执行的是第 %d 条", len(all), i+1),
	}, true
}

// spanTexts is SplitStatements over already-split statements.
func spanTexts(spans []db.StmtSpan) []string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, s.Text)
	}
	return out
}

// spanIndex finds the position of the statement starting at start.
func spanIndex(spans []db.StmtSpan, start int) int {
	for i, s := range spans {
		if s.Start == start {
			return i
		}
	}
	return -1
}

// selectionRange recovers where the selected text starts. fyne gives the
// selected TEXT but not its offsets, so the caret and the selected length are
// all there is to go on - and the caret sits at whichever end the drag ended
// on, hence both attempts. It reports false when neither reading matches, which
// repeated text ("SELECT 1; SELECT 1") can make ambiguous; the caller then runs
// the selection as its own script rather than guessing wrong.
func selectionRange(text, sel string, caret int) (start, end int, ok bool) {
	if sel == "" || caret < 0 {
		return 0, 0, false
	}
	n := len(sel)
	if caret >= n && text[caret-n:caret] == sel {
		return caret - n, caret, true
	}
	if caret+n <= len(text) && text[caret:caret+n] == sel {
		return caret, caret + n, true
	}
	return 0, 0, false
}

// selectionNote explains that only part of the tab ran, which is the one thing
// the result line cannot say by itself - 返回 3 行 looks the same either way.
func selectionNote(ran, total int) string {
	if total <= 1 {
		return ""
	}
	return fmt.Sprintf(" · 编辑器共 %d 条语句，按选区执行了其中 %d 条", total, ran)
}

// runStatements executes the resolved statements and renders what they end with.
// The query runs on a background goroutine so the window stays responsive; a
// cancel button aborts it, and the result is marshalled back to the UI thread
// before the grid updates.
func (c *Client) runStatements(plan runPlan) {
	stmts := plan.stmts
	connID, dbName, pageSize := c.querySession()
	conn, err := c.store.OpenConn(connID)
	if err != nil {
		c.setStatusError("错误：" + err.Error())
		return
	}
	limit := c.limitValue()
	// Paging covers one statement only; a script's last result is whatever the
	// script ended with, which is not something a page bar can re-run honestly.
	paged := c.page.on && len(stmts) == 1 && db.Pageable(conn.Config.Type, stmts[0])
	var pageNote string

	busy := "执行中 ...（点「取消」可中断）"
	if plan.busy != "" {
		busy = plan.busy + " ...（点「取消」可中断）"
	}
	c.runTracked(busy,
		func(ctx context.Context) (db.QueryResult, string) {
			// The whole query + history-record runs off the UI thread.
			if paged {
				// Making the window stable reads the catalog, so it belongs
				// here rather than on the UI thread.
				stmt, note := c.stablePageQuery(ctx, conn, dbName, stmts[0])
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
			c.setEditContext(lastStmt, res)
			c.renderResult(res)
			c.afterRunPaging(lastStmt, res, pageNote, len(stmts) == 1)
			if !c.page.on && res.Error == "" && len(res.Columns) > 0 {
				// Paging off means the toolbar's 行数上限 is the user's stated
				// intent; say so whenever the answer and that intent disagree -
				// cut short, or the statement never taking the cap at all.
				c.appendStatus(rowCapNote(conn.Config.Type, lastStmt, limit, res.Truncated))
			}
			c.appendStatus(plan.note)
			c.refreshHistory()
			if plan.whole {
				c.clearTabDirty(c.tabs.entry)
			}
			if canceled && res.Error != "" {
				c.setStatus("查询已取消")
			}
			c.focusEditor()
		})
}

// rowCapNote phrases the two outcomes the row count cannot explain by itself:
// the cap bound and cut the answer short, so what is on screen is only the first
// slice; or the cap ran but the SQL was left untouched, so the grid is not what
// the selector asked for either. Empty when neither applies - the cap bound and
// the whole answer fit, or 无限制 was chosen.
func rowCapNote(dbType, stmt string, limit int, truncated bool) string {
	if truncated {
		// Only a cap the client injected can truncate, so this is the one case
		// where the row count is known to be a prefix rather than the story.
		return fmt.Sprintf(" · 已达行数上限 %d，还有更多行未取出", limit)
	}
	switch db.JudgeRowCap(dbType, stmt, limit) {
	case db.RowCapOwnWindow:
		return fmt.Sprintf(" · 语句自带行窗口，行数上限 %d 未生效", limit)
	case db.RowCapShapeUnsafe:
		return " · 该语句形状无法安全注入行数上限，返回多少行就是多少行"
	}
	return ""
}

// runTracked runs one query round with the 取消 button wired to it: work runs on
// a background goroutine (inline when asyncRun is false, for deterministic
// tests) and finish receives its result on the UI thread. runCurrent and
// runPage share the cancellation, busy-button and marshalling bookkeeping.
//
// It reports whether the round started: a run that is still in flight owns the
// cancellation and the result grid, so a second one is refused rather than
// allowed to overwrite it.
func (c *Client) runTracked(busy string, work func(context.Context) (db.QueryResult, string),
	finish func(db.QueryResult, string, bool)) bool {
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		c.setStatusWarn("已有查询在执行，请等待或点「取消」")
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.busy = true
	c.runGen++ // 新一轮开跑：正在流式渲染的旧批次据此作废
	c.cancelFn = cancel
	c.mu.Unlock()
	c.setRunning(true)
	c.setStatus(busy)

	// release belongs to runTracked, not to the callers: every finish callback
	// used to clear cancelFn for itself, which is how a round that was no longer
	// the current one could take the 取消 button away. Owning it here, together
	// with the guard above, means exactly one round is ever in flight - so the
	// cancellation the button holds is always the one that can still be stopped.
	release := func() {
		c.mu.Lock()
		c.busy = false
		c.cancelFn = nil
		c.mu.Unlock()
		c.setRunning(false)
	}

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
		release()
		finish(res, lastStmt, canceled)
		return true
	}
	go func() {
		res, lastStmt, canceled := run()
		c.dispatch(func() {
			release()
			finish(res, lastStmt, canceled)
		})
	}()
	return true
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

// setRunning toggles every control that starts or redirects a run: the run and
// cancel buttons, and the whole paging bar. A page turn is a re-query of the
// same statement, so it is exactly as unwelcome mid-run as a second 执行 - and
// the bar is the worse one to leave live, because the run it starts describes a
// window nobody has asked for any more.
func (c *Client) setRunning(running bool) {
	idle := !running
	toggle := func(w fyne.Disableable, enabled bool) {
		if w == nil {
			return
		}
		if enabled {
			w.Enable()
			return
		}
		w.Disable()
	}
	toggle(c.runBtn, idle)
	toggle(c.cancelRunBtn, running)
	toggle(c.page.prevBtn, idle)
	toggle(c.page.nextBtn, idle)
	toggle(c.page.sizeSel, idle)
	toggle(c.page.check, idle)
}
