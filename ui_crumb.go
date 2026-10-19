package main

// 面包屑栏：内容区网格上方的一行 —— 左侧 < >、中间居中圆角框显示当前表名，
// 右侧留给详情栏开关（P2 挂上）。
//
// 移植自内置版 workspace.go:880 buildCrumbBar，但两处刻意不同：
//  1. 翻页复用独立版的服务端分页 stepPage（PK 注入稳定排序），不用内置版写死的
//     LIMIT/OFFSET —— 见 ui_paging.go，那套分页比内置版强，保留独立版实现；
//  2. 表名读独立版的 gridState.src（db.TableRef），不照搬内置版 MySQL-only 的
//     information_schema 查询，因此 5 个引擎都出得来表名。

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// crumbState 是面包屑栏的控件集合。
type crumbState struct {
	bar  *fyne.Container
	box  fyne.CanvasObject
	name *widget.Label
	prev *widget.Button
	next *widget.Button
	// detailsBtn 是右端的详情栏开关（P2 挂上，见 ui_details.go）。
	detailsBtn *widget.Button
}

// buildCrumbBar 组装面包屑行：左 < > 翻页、居中圆角表名框。
// 右侧目前留空，P2 的详情栏开关会挂到这里。
func (c *Client) buildCrumbBar() fyne.CanvasObject {
	c.crumb.prev = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() { c.stepPage(-1) })
	c.crumb.next = widget.NewButtonWithIcon("", theme.NavigateNextIcon(), func() { c.stepPage(1) })
	c.crumb.prev.Importance = widget.LowImportance
	c.crumb.next.Importance = widget.LowImportance

	c.crumb.name = widget.NewLabel("—")
	c.crumb.name.Truncation = fyne.TextTruncateEllipsis
	c.crumb.box = crumbBox(c.crumb.name)

	c.crumb.detailsBtn = widget.NewButton("详情", c.toggleDetails)
	c.crumb.detailsBtn.Importance = widget.LowImportance

	c.crumb.bar = container.NewBorder(
		nil, nil,
		container.NewHBox(c.crumb.prev, c.crumb.next), // 左：< >
		c.crumb.detailsBtn,               // 右：详情栏开关
		container.NewCenter(c.crumb.box), // 中：表名
	)
	// 建好即同步一次初始态：还没有结果时表名显示占位符、< > 禁用。
	c.refreshCrumb()
	return c.crumb.bar
}

// crumbBox 是居中圆角框：底色 + 描边 + 内边距包裹的内容。
// 内置版同款结构（canvas.Rectangle 圆角 + 文本），配色改走 theme.go 的命名色。
func crumbBox(content fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(crumbBG())
	bg.CornerRadius = 6
	border := canvas.NewRectangle(color.Transparent)
	border.CornerRadius = 6
	border.StrokeColor = crumbBorder()
	border.StrokeWidth = 1
	return container.NewStack(bg, border, container.NewPadded(content))
}

// refreshCrumb 把面包屑同步到当前结果：表名取 grid.src，翻页按钮随结果是否
// 可翻页启停。控件尚未建好时（早期调用）直接返回。
func (c *Client) refreshCrumb() {
	if c.crumb.name == nil {
		return
	}
	name := c.grid.src.Table
	if name == "" {
		name = "—"
	}
	c.crumb.name.SetText(name)

	if c.crumb.prev == nil {
		return
	}
	if c.page.stmt == "" {
		c.crumb.prev.Disable()
		c.crumb.next.Disable()
		return
	}
	c.crumb.prev.Enable()
	c.crumb.next.Enable()
}
