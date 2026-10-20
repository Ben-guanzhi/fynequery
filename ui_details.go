package main

// 右侧详情栏（P2）：移植自内置版 details.go 的 TablePlus 右侧「详情」面板。
//
// 两处刻意与内置版不同：
//  1. 数据获取走独立版 internal/db 的目录方法（Columns / TableIndexes /
//     ForeignKeys），跨 5 个引擎生效；不照搬内置版写死 MySQL
//     information_schema.TABLES 的查询 —— 那条路在非 MySQL 上恒为「—」。
//  2. 因此表模式展示的是跨引擎都拿得到的目录计数（字段/索引/外键），而不是
//     data_size / index_size 这类只有 MySQL 才有的统计量。
//
// 行模式（选中了一行）才是这个面板的主用途：列出该行每个字段的值，点击可编辑。

import (
	"context"
	"image/color"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// detailsField 是详情栏的一行：字段名、类型、值。
// Col 是该字段在结果网格里的列号，-1 表示这一行不来自结果集（只可读）。
type detailsField struct {
	Name     string
	Type     string
	Value    *string
	Col      int
	ReadOnly bool
}

// detailsState 是详情栏面板与其当前内容。
type detailsState struct {
	pane  *fyne.Container
	body  *fyne.Container
	title *widget.Label
	// tabs 是面板顶部的「详情 / 索引」切换；indexBody 是索引标签的内容容器
	// （渲染见 ui_index.go）。
	tabs      *container.AppTabs
	indexBody *fyne.Container
	// on 记录面板当前是否可见（Hide/Show 之外还要能回答「该不该刷新」）。
	on bool
}

// currentDetailsMode 由当前选中的标签决定，不另存一份模式字段：缓存模式会和
// AppTabs 的实际选中项脱节（SelectTabIndex 不触发 OnSelected），所以每次读。
func (c *Client) currentDetailsMode() detailsMode {
	if c.details.tabs != nil && c.details.tabs.SelectedIndex() == detailTabIndex {
		return detailsModeIndex
	}
	return detailsModeInfo
}

// buildDetailsPane 组装右侧详情栏：标题 + 分隔线 + 可滚动属性区。
// 默认隐藏，由面包屑右侧的「详情」按钮（toggleDetails）打开。
func (c *Client) buildDetailsPane() fyne.CanvasObject {
	c.details.title = widget.NewLabel("详情")
	c.details.title.TextStyle = fyne.TextStyle{Bold: true}
	c.details.body = container.NewVBox()
	c.details.indexBody = container.NewVBox()

	infoTab := container.NewBorder(
		container.NewVBox(container.NewPadded(c.details.title), widget.NewSeparator()),
		nil, nil, nil,
		container.NewPadded(container.NewVScroll(c.details.body)),
	)
	indexTab := container.NewPadded(container.NewVScroll(c.details.indexBody))

	c.details.tabs = container.NewAppTabs(
		container.NewTabItem("详情", infoTab),
		container.NewTabItem("索引", indexTab),
	)
	c.details.tabs.OnSelected = func(item *container.TabItem) {
		c.refreshDetails()
	}

	c.details.pane = container.NewStack(c.details.tabs)
	if c.uiSet.DetailsOn {
		c.details.on = true
	} else {
		c.details.pane.Hide()
	}
	return c.details.pane
}

// toggleDetails 显隐右侧详情栏；打开时立即按当前选择刷一次内容。
func (c *Client) toggleDetails() {
	if c.details.pane == nil {
		return
	}
	c.details.on = !c.details.on
	c.uiSet.DetailsOn = c.details.on
	if !c.details.on {
		c.details.pane.Hide()
		return
	}
	c.details.pane.Show()
	c.refreshDetails()
}

// refreshDetails 按当前选择重画详情栏：有选中行 → 该行字段值；否则 → 表目录信息。
func (c *Client) refreshDetails() {
	if c.details.pane == nil || !c.details.on {
		return
	}
	if c.currentDetailsMode() == detailsModeIndex {
		c.setDetailsIndexes()
		return
	}
	if c.grid.hasCell && c.grid.cell.Row >= 0 && c.grid.cell.Row < len(c.grid.rows) {
		c.setDetailsRow()
		return
	}
	c.setDetailsTable()
}

// setDetailsRow 展示选中行每个字段的值：可点开编辑，chevron 给快捷操作。
func (c *Client) setDetailsRow() {
	c.details.title.SetText("详情 · 选中行")
	row := c.grid.cell.Row
	c.details.body.Objects = nil
	for i, name := range c.grid.cols {
		var val *string
		if i < len(c.grid.rows[row]) {
			v := c.grid.rows[row][i]
			val = &v
		}
		typ := ""
		if i < len(c.grid.colTypes) {
			typ = c.grid.colTypes[i]
		}
		c.details.body.Add(c.detailsFieldRow(detailsField{
			Name:     name,
			Type:     typ,
			Value:    val,
			Col:      i,
			ReadOnly: !c.gridEditable(),
		}, row))
	}
	c.details.body.Refresh()
}

// setDetailsTable 在没有选中行时展示当前表的目录计数（跨引擎，失败显示 —）。
func (c *Client) setDetailsTable() {
	src := c.grid.src
	if src.Table == "" {
		c.details.title.SetText("详情")
		c.details.body.Objects = []fyne.CanvasObject{
			hintLabel("打开一个表并选中一行，这里会显示该行每个字段的值。"),
		}
		c.details.body.Refresh()
		return
	}
	c.details.title.SetText("详情 · " + src.Table)
	c.details.body.Objects = []fyne.CanvasObject{hintLabel("加载中 ...")}
	c.details.body.Refresh()

	var (
		nCols, nIdx, nFk int
		eCols, eIdx, eFk error
	)
	c.background(func(ctx context.Context) {
		cols, err := c.store.Columns(ctx, c.currentID, src.Database, src.Schema, src.Table)
		nCols, eCols = len(cols), err
		idx, err := c.store.TableIndexes(ctx, c.currentID, src.Database, src.Schema, src.Table)
		nIdx, eIdx = len(idx), err
		fks, err := c.store.ForeignKeys(ctx, c.currentID, src.Database, src.Schema, src.Table)
		nFk, eFk = len(fks), err
	}, func() {
		// 取目录期间面板可能已被关掉、切到「索引」、或者用户已选中一行。
		if !c.details.on || c.currentDetailsMode() != detailsModeInfo || c.grid.hasCell {
			return
		}
		c.details.body.Objects = nil
		add := func(name string, n int, err error) {
			v := "—"
			if err == nil {
				v = strconv.Itoa(n)
			}
			c.details.body.Add(c.detailsFieldRow(detailsField{
				Name: name, Value: &v, Col: -1, ReadOnly: true,
			}, -1))
		}
		add("字段数", nCols, eCols)
		add("索引数", nIdx, eIdx)
		add("外键数", nFk, eFk)
		c.details.body.Refresh()
	})
}

// detailsFieldRow 是一行属性：左字段名（带类型）、右数值框。
// 可写字段用 valueButton（点击编辑 + chevron 菜单），只读字段用 staticBox。
func (c *Client) detailsFieldRow(f detailsField, row int) fyne.CanvasObject {
	name := widget.NewLabel(f.Name)
	name.TextStyle = fyne.TextStyle{Bold: true}
	name.Truncation = fyne.TextTruncateEllipsis
	sub := widget.NewLabel(f.Type)
	sub.Importance = widget.LowImportance
	sub.Truncation = fyne.TextTruncateEllipsis
	left := container.NewVBox(name, sub)

	if f.ReadOnly || f.Col < 0 {
		return container.NewBorder(nil, nil, left, nil, staticBox(f.Value))
	}
	vb := newValueButton(f.Value)
	vb.onEdit = func() {
		c.grid.table.Select(widget.TableCellID{Row: row, Col: f.Col})
		c.editSelectedCell()
	}
	vb.onMenu = func(pos fyne.Position) { c.showQuickValues(f, row, pos) }
	return container.NewBorder(nil, nil, left, nil, vb)
}

// showQuickValues 是数值框右侧 chevron 的菜单。
//
// 只放两条走既有写入路径的安全动作：复制值、打开编辑器。内置版的
// NULL / 空串 / NOW() 直接赋值需要一条「不经对话框改 edits 队列」的入口，
// 独立版目前没有（editSelectedCell 是对话框驱动的），因此暂不搬，留待补上
// 程序化 setCell 后再加。
func (c *Client) showQuickValues(f detailsField, row int, pos fyne.Position) {
	if f.Col < 0 || row < 0 {
		return
	}
	menu := fyne.NewMenu("",
		fyne.NewMenuItem("复制值", func() {
			c.grid.table.Select(widget.TableCellID{Row: row, Col: f.Col})
			c.copyCell()
		}),
		fyne.NewMenuItem("编辑 ...", func() {
			c.grid.table.Select(widget.TableCellID{Row: row, Col: f.Col})
			c.editSelectedCell()
		}),
	)
	m := widget.NewPopUpMenu(menu, c.win.Canvas())
	m.ShowAtPosition(pos)
}

// hintLabel 是详情栏里的斜体说明文字（移植自内置版 details.go:108）。
func hintLabel(s string) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	l.Wrapping = fyne.TextWrapWord
	return l
}

// staticBox 是只读数值框：没有点击、没有 chevron。
func staticBox(v *string) fyne.CanvasObject {
	text := "NULL"
	italic := true
	if v != nil {
		text = *v
		italic = false
	}
	lbl := widget.NewLabel(text)
	lbl.Truncation = fyne.TextTruncateEllipsis
	if italic {
		lbl.TextStyle.Italic = true
		lbl.Importance = widget.LowImportance
	}
	return valueBox(container.NewPadded(lbl))
}

// valueButton 是字段名下方的圆角数值框：点文字进编辑器，点最右侧 chevron
// 出快捷菜单（内置版 details.go:58 的同款命中判定：右起 24px 归 chevron）。
type valueButton struct {
	widget.BaseWidget
	value  *string
	onEdit func()
	onMenu func(pos fyne.Position)
	label  *widget.Label
}

func newValueButton(v *string) *valueButton {
	vb := &valueButton{value: v}
	vb.ExtendBaseWidget(vb)
	return vb
}

// valueBox 是圆角底 + 描边 + 内容的公共外壳。
func valueBox(content fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(crumbBG())
	bg.CornerRadius = 6
	border := canvas.NewRectangle(color.Transparent)
	border.CornerRadius = 6
	border.StrokeColor = crumbBorder()
	border.StrokeWidth = 1
	return container.NewStack(bg, border, content)
}

func (vb *valueButton) CreateRenderer() fyne.WidgetRenderer {
	vb.label = widget.NewLabel("")
	vb.label.Truncation = fyne.TextTruncateEllipsis
	if vb.value == nil {
		vb.label.SetText("NULL")
		vb.label.TextStyle.Italic = true
		vb.label.Importance = widget.LowImportance
	} else {
		vb.label.SetText(*vb.value)
	}
	chevron := widget.NewIcon(theme.MenuDropDownIcon())
	row := container.NewBorder(nil, nil, nil, chevron, container.NewPadded(vb.label))
	return widget.NewSimpleRenderer(valueBox(row))
}

func (vb *valueButton) Tapped(e *fyne.PointEvent) {
	// 最右 24px 是 chevron 的命中区。
	if vb.Size().Width > 0 && e.Position.X >= vb.Size().Width-24 {
		if vb.onMenu != nil {
			vb.onMenu(e.AbsolutePosition)
		}
		return
	}
	if vb.onEdit != nil {
		vb.onEdit()
	}
}
