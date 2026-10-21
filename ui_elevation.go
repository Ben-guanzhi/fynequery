package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

// edgeSide says which edge of the wrapped content carries the elevation shadow.
type edgeSide int

const (
	edgeTop edgeSide = iota
	edgeBottom
	edgeLeft
	edgeRight
)

// elevatedEdge 把一个内容对象包成「任一侧带主题阴影」的容器，用于 tyde / Material
// 式的表面分层：工具栏底、侧栏右、详情栏左各压一道极淡阴影，把面板从内容区托起。
//
// 阴影是一块 2px 的主题色矩形（见 edgeShadow），作为 Border 的该边子节点。canvas
// 矩形的 MinSize 默认是 1×1，这里显式设成 2×2，于是这条边固定占 2px，而 center
// 的 content 尺寸一字未动 —— 任何 widget 的 MinSize 都没变，layout_test.go 锁定的
// 几何断言因此继续全绿。矩形登记进 c.themedRects，明暗主题切换时由 refreshThemeColors
// 重绘，不会在深色主题下留下一道浅色旧影。
//
// 用硬边 2px 暗线而非原生模糊阴影是刻意的：fyne 2.8 的 canvas.Rectangle.Shadow 虽
// 能出柔和投影，但是否被当前驱动渲染取决于运行环境；这道关键线（keyline）则一定会
// 画出来，且读感上正是 Material 的分层暗示。要更柔和的投影，将来把这块矩形换成带
// Shadow 字段的透明矩形即可，无需动布局。
func (c *Client) elevatedEdge(side edgeSide, content fyne.CanvasObject) *fyne.Container {
	shadow := canvas.NewRectangle(edgeShadow())
	shadow.SetMinSize(fyne.NewSize(2, 2))
	c.themedRects = append(c.themedRects, themedRect{rect: shadow, fill: edgeShadow})

	var top, bottom, left, right fyne.CanvasObject
	switch side {
	case edgeTop:
		top = shadow
	case edgeBottom:
		bottom = shadow
	case edgeLeft:
		left = shadow
	case edgeRight:
		right = shadow
	}
	return container.NewBorder(top, bottom, left, right, content)
}
