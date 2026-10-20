package main

// 主题配色：面包屑框、右侧详情栏、分隔线。
//
// 独立版原本没有 theme.go（配色一律走 fyne 内置主题），内置版则有一份写死
// 深色值的 theme.go（#252525/#2D2D2D 系）。这里取中间路线：新增这组命名取
// 值函数，但颜色全部来自 fyne 的命名色，因此明暗主题自动适配，不需要像内置
// 版那样维护两套调色板。

import (
	"image/color"

	"fyne.io/fyne/v2/theme"
)

// crumbBG 是面包屑框与详情栏数值框的底色。
func crumbBG() color.Color { return theme.Color(theme.ColorNameInputBackground) }

// crumbBorder 是面包屑框与详情栏数值框的描边色。
func crumbBorder() color.Color { return theme.Color(theme.ColorNameShadow) }

// detailsBG 是右侧详情栏面板的底色。
func detailsBG() color.Color { return theme.Color(theme.ColorNameMenuBackground) }

// zebraColor 是结果网格隔行条纹的颜色：前景色压到很低的 alpha 叠在底色上。
// 取前景而不是某个固定浅灰，是为了明暗主题下都只轻微偏移背景；半透明则保证
// 表格自己的选中高亮从条纹底下透出来，不会被不透明条纹盖掉。
func zebraColor() color.Color {
	fg := theme.Color(theme.ColorNameForeground)
	r, g, b, _ := fg.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 14}
}
