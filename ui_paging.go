package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// This file is result paging: the 分页浏览 switch in the toolbar, the page bar
// over the grid, and the re-query that fetches one window of rows.
//
// Paging is server-side. Each page re-runs the same statement with a row window
// (db.PageSQL, rendered by the engine's dialect), so there is no hidden COUNT
// and no whole-table fetch. The bar therefore reports the page and the rows it
// actually got, and 下一页 follows the reference client's rule: a full page
// means there is very likely another one behind it, a short page is the last.
// The trade-off to state plainly: the in-result search and the header sort work
// on the rows on screen, i.e. the current page only.

// pageSizes are the 每页 choices. The widest one stays well under
// db.MaxPageSize, the hard cap PageSQL enforces.
var pageSizes = []string{"10", "20", "50", "100", "200", "500"}

const defaultPageSize = 100

// buildPageBar assembles the strip over the grid: page and row count on the
// left, the window size and the turn buttons on the right. It starts hidden
// because paging is opt-in and only a pageable result shows it.
func (c *Client) buildPageBar() *fyne.Container {
	if !validPageSize(c.page.size) {
		c.page.size = defaultPageSize
	}
	c.page.label = widget.NewLabel("")
	c.page.label.Importance = widget.LowImportance

	c.page.sizeSel = widget.NewSelect(pageSizes, c.onPageSizeChange)
	c.page.sizeSel.SetSelected(strconv.Itoa(c.page.size))

	c.page.prevBtn = widget.NewButtonWithIcon("上一页", theme.NavigateBackIcon(), func() { c.stepPage(-1) })
	c.page.prevBtn.Importance = widget.LowImportance
	c.page.nextBtn = widget.NewButtonWithIcon("下一页", theme.NavigateNextIcon(), func() { c.stepPage(1) })
	c.page.nextBtn.Importance = widget.LowImportance

	note := widget.NewLabel("搜索与排序仅作用于当前页")
	note.Importance = widget.LowImportance

	c.page.bar = container.NewBorder(nil, nil, c.page.label,
		container.NewHBox(note, widget.NewSeparator(), c.page.sizeSel, c.page.prevBtn, c.page.nextBtn), nil)
	c.page.bar.Hide()
	return c.page.bar
}

// buildPagingCheck is the toolbar switch that hands the row cap over to the
// page window. While it is on, 行数上限 no longer applies to a pageable
// SELECT - the window is the cap - so the selector is greyed out.
func (c *Client) buildPagingCheck() *widget.Check {
	check := widget.NewCheck("分页浏览", func(on bool) {
		c.page.on = on
		if on {
			c.page.limitSel.Disable()
			return
		}
		c.page.limitSel.Enable()
		c.page.stmt = ""
		c.page.note = ""
		c.page.no = 1
		c.refreshPageBar()
	})
	return check
}

// validPageSize accepts only the sizes the selector offers, so a ui.json
// holding a stale or hand-edited value falls back to the default.
func validPageSize(n int) bool {
	for _, s := range pageSizes {
		if v, err := strconv.Atoi(s); err == nil && v == n {
			return true
		}
	}
	return false
}

// pageEligible decides whether the result just shown can be browsed as row
// windows, and explains why not when 分页浏览 is on but the statement refuses.
// singleStatement is what the run actually executed: a script's last result is
// whatever the script ended with, and re-running just that statement as page 2
// would replay rows the script had already shown, so the bar must not claim a
// page the run never fetched.
func (c *Client) pageEligible(stmt string, res db.QueryResult, singleStatement bool) (bool, string) {
	if !c.page.on || res.Error != "" {
		return false, ""
	}
	cfg, ok := c.store.LiveConfig(c.currentID)
	if !ok {
		return false, "连接已断开"
	}
	if !db.Pageable(cfg.Type, stmt) {
		return false, "分页浏览只支持本身不带行窗口（LIMIT/OFFSET/TOP/FETCH/ROWNUM）的 SELECT"
	}
	if !singleStatement {
		return false, "分页浏览一次只认一条语句"
	}
	return true, ""
}

// stablePageQuery rewrites a pageable statement so consecutive windows cannot
// show one row twice or skip it. A statement that already sorts its own rows is
// left alone; otherwise the primary key of a single-table SELECT * becomes the
// sort, which is what the reference client's pageOrder does. A projection or an
// aggregate is only reported as unstable - inventing a sort there would change
// what the query means. It returns the statement to page plus what the bar
// should say about it, and reads the catalog, so it runs off the UI thread.
func (c *Client) stablePageQuery(ctx context.Context, conn *db.Conn, database, stmt string) (string, string) {
	const unstable = " · ⚠ 排序不稳定，翻页可能重复或漏行"
	if !db.PageNeedsStableOrder(stmt) {
		return stmt, ""
	}
	ref := c.store.QuerySource(conn.Config.ID, stmt, database)
	if ref.Table == "" {
		return stmt, unstable
	}
	schema := db.DefaultSchema(conn.Config.Type, ref.Schema)
	cols, err := c.store.Columns(ctx, ref.ConnID, ref.Database, schema, ref.Table)
	if err != nil {
		return stmt, unstable + "（读不到主键）"
	}
	var pk []string
	for _, col := range cols {
		if col.IsPrimaryKey {
			pk = append(pk, col.Name)
		}
	}
	if len(pk) == 0 {
		return stmt, unstable + "（无主键）"
	}
	ordered, err := db.WithOrderBy(conn.Config.Type, stmt, pk)
	if err != nil {
		return stmt, unstable
	}
	// The bar is one row wide and its right-hand controls must never be pushed
	// out of reach (the toolbar's old clipping bug), so the key is only named
	// while it fits a small budget; a wider one is counted instead. The SQL
	// always sorts by every key column.
	const maxListed = 3
	note := strings.Join(pk, ", ")
	if len(pk) > maxListed || displayWidth(note) > 24 {
		note = fmt.Sprintf("%d 列", len(pk))
	}
	return ordered, " · 已按主键 " + note + " 排序"
}

// afterRunPaging re-points the page state at the statement that produced the
// result now on screen, then refreshes the bar. Called after every full run:
// executing is always page 1, and note is what stablePageQuery reported.
func (c *Client) afterRunPaging(stmt string, res db.QueryResult, note string, singleStatement bool) {
	c.page.no = 1
	c.page.stmt = ""
	c.page.note = ""
	if ok, why := c.pageEligible(stmt, res, singleStatement); ok {
		c.page.stmt = stmt
		c.page.note = note
	} else if why != "" {
		c.setStatus(why + "，本次结果未分页")
	}
	c.refreshPageBar()
}

// refreshPageBar redraws the bar from the current page state. Nothing to page
// means nothing to show.
func (c *Client) refreshPageBar() {
	if c.page.bar == nil {
		return
	}
	if c.page.stmt == "" {
		c.page.bar.Hide()
		return
	}
	rows := len(c.grid.rows)
	c.page.label.SetText(fmt.Sprintf("第 %d 页 · 本页 %d 行%s", c.page.no, rows, c.page.note))
	if c.page.no <= 1 {
		c.page.prevBtn.Disable()
	} else {
		c.page.prevBtn.Enable()
	}
	if rows < c.page.size {
		c.page.nextBtn.Disable()
	} else {
		c.page.nextBtn.Enable()
	}
	c.page.bar.Show()
}

// onPageSizeChange records the new window size and, when a paged result is
// already on screen, re-fetches page 1 with it right away.
func (c *Client) onPageSizeChange(s string) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return
	}
	c.page.size = n
	c.uiSet.PageSize = n
	if c.page.stmt == "" {
		c.setStatus(fmt.Sprintf("已设为每页 %d 行", n))
		return
	}
	if c.grid.edits.any() {
		c.setStatus(fmt.Sprintf("已设为每页 %d 行；有未保存的更改，先保存或放弃再翻页", n))
		return
	}
	c.runPage(1)
}

// stepPage turns to the neighbouring window of the same statement.
func (c *Client) stepPage(delta int) {
	if c.page.stmt == "" {
		return
	}
	if c.grid.edits.any() {
		c.setStatus("有未保存的更改，先保存或放弃再翻页")
		return
	}
	target := c.page.no + delta
	if target < 1 {
		return
	}
	c.runPage(target)
}

// runPage re-runs the paged statement for one row window. A page turn is not a
// new statement, so it records no history entry, leaves the tab's modified
// marker alone, and on failure keeps the page that is on screen.
func (c *Client) runPage(pageNo int) {
	connID, dbName, pageSize := c.querySession()
	conn, err := c.store.OpenConn(connID)
	if err != nil {
		c.setStatus("错误：" + err.Error())
		return
	}
	stmt := c.page.stmt
	c.runTracked("翻页中 ...（点「取消」可中断）",
		func(ctx context.Context) (db.QueryResult, string) {
			return c.store.ExecutePage(ctx, conn, dbName, stmt, pageNo, pageSize), stmt
		},
		func(res db.QueryResult, _ string, canceled bool) {
			if res.Error != "" {
				c.renderResult(res)
				if canceled {
					c.setStatus("翻页已取消")
				}
				c.refreshPageBar()
				return
			}
			c.setEditContext(stmt, res)
			c.renderResult(res)
			c.page.no = pageNo
			c.refreshPageBar()
		})
}
