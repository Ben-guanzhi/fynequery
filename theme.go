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

// dividerColor 是详情栏属性行之间的分隔线颜色。
func dividerColor() color.Color { return theme.Color(theme.ColorNameSeparator) }
