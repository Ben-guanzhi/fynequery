package main

// 主题配色。三级灰白是这一版外观的主干，取自 TablePlus 的浅色版：
//
//	窗口底  浅灰 #F2F3F5   —— 侧栏与工具条的天地
//	面板    再浅 #F7F8F9   —— 详情栏、面包屑、菜单
//	内容区  纯白 #FFFFFF   —— 编辑器与结果网格
//
// 中间只隔 1px 的 #D8DBDE 分隔线，颜色之外的空间一律不动（字号、行高、padding
// 都是既有测试的阈值）。
//
// 早先这里只有几个「取 fyne 命名色」的别名函数，配色因此是 fyne 内置主题给的。
// 现在反过来：颜色由本文件定义，命名色只是它们的名字 —— 这样明暗两套值放在同一
// 张表里，改一个色不会只改到一半。
//
// 颜色与尺寸都收在 tpColor / tpSize 两个纯函数里：它们不碰 fyne.CurrentApp()，
// 所以调色板能在不启动 App 的情况下单测（见 theme_test.go）。

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
)

// tpFollow marks the "跟随系统" variant. It is not one of fyne's two variants
// (VariantDark is 0 and VariantLight is 1 - the zero value is dark), so a
// sentinel has to be a third value or "follow" would be indistinguishable from
// "always dark".
const tpFollow fyne.ThemeVariant = 2

// ---- palette: the whole look, in one table ----

// tpInk is one colour of the palette. opaque is the common case; the
// translucent ones (selections, hover, shadows) are stated as an alpha over
// whatever is under them, which is what keeps the grid's selection highlight
// visible through a zebra stripe.
type tpInk struct {
	RGB   uint32 // 0xRRGGBB
	alpha uint8  // 0 = opaque
}

func ink(hex uint32) tpInk { return tpInk{RGB: hex} }

func inkA(hex uint32, alpha uint8) tpInk { return tpInk{RGB: hex, alpha: alpha} }

func (i tpInk) color() color.Color {
	if i.alpha == 0 {
		return color.NRGBA{
			R: uint8(i.RGB >> 16), G: uint8(i.RGB >> 8), B: uint8(i.RGB), A: 0xFF,
		}
	}
	return color.NRGBA{
		R: uint8(i.RGB >> 16), G: uint8(i.RGB >> 8), B: uint8(i.RGB), A: i.alpha,
	}
}

// tpPrimary is the accent. Everything blue in the window answers to it.
const tpPrimary = 0x1E6FD9

// tpPrimaryDark is the accent in the dark variant, lifted towards the light end
// so it still reads as a highlight against a near-black background.
const tpPrimaryDark = 0x4C8FF7

// tpLight / tpDark are the two palettes; every name below appears in both, so a
// colour can never be "the new one" in one variant and "the old one" in the
// other. Names absent from these maps fall through to the built-in theme.
var tpLight = map[fyne.ThemeColorName]tpInk{
	theme.ColorNameBackground:          ink(0xF2F3F5),
	theme.ColorNameMenuBackground:      ink(0xF7F8F9),
	theme.ColorNameInputBackground:     ink(0xFFFFFF),
	theme.ColorNameOverlayBackground:   ink(0xFFFFFF),
	theme.ColorNameHeaderBackground:    ink(0xF7F8F9),
	theme.ColorNameButton:              ink(0xFFFFFF),
	theme.ColorNameDisabledButton:      ink(0xEDEEF0),
	theme.ColorNameSeparator:           ink(0xD8DBDE),
	theme.ColorNameInputBorder:         ink(0xD8DBDE),
	theme.ColorNameShadow:              inkA(0x000000, 0x18),
	theme.ColorNamePrimary:             ink(tpPrimary),
	theme.ColorNameHyperlink:           ink(tpPrimary),
	theme.ColorNameForegroundOnPrimary: ink(0xFFFFFF),
	theme.ColorNameFocus:               inkA(tpPrimary, 0x33),
	theme.ColorNameSelection:           inkA(tpPrimary, 0x2E),
	theme.ColorNameHover:               inkA(0x000000, 0x0A),
	theme.ColorNamePressed:             inkA(0x000000, 0x14),
	theme.ColorNameForeground:          ink(0x33333A),
	theme.ColorNamePlaceHolder:         ink(0x9AA0A6),
	theme.ColorNameDisabled:            ink(0xB6BAC0),
	theme.ColorNameScrollBar:           inkA(0x000000, 0x52),
	theme.ColorNameScrollBarBackground: ink(0xE9EAEC),
}

var tpDark = map[fyne.ThemeColorName]tpInk{
	theme.ColorNameBackground:          ink(0x191A1C),
	theme.ColorNameMenuBackground:      ink(0x212225),
	theme.ColorNameInputBackground:     ink(0x212225),
	theme.ColorNameOverlayBackground:   ink(0x1E1F21),
	theme.ColorNameHeaderBackground:    ink(0x1E1F21),
	theme.ColorNameButton:              ink(0x2A2C2F),
	theme.ColorNameDisabledButton:      ink(0x232428),
	theme.ColorNameSeparator:           ink(0x37383B),
	theme.ColorNameInputBorder:         ink(0x3A3B3E),
	theme.ColorNameShadow:              inkA(0x000000, 0x55),
	theme.ColorNamePrimary:             ink(tpPrimaryDark),
	theme.ColorNameHyperlink:           ink(tpPrimaryDark),
	theme.ColorNameForegroundOnPrimary: ink(0xFFFFFF),
	theme.ColorNameFocus:               inkA(tpPrimaryDark, 0x40),
	theme.ColorNameSelection:           inkA(tpPrimaryDark, 0x33),
	theme.ColorNameHover:               inkA(0xFFFFFF, 0x0A),
	theme.ColorNamePressed:             inkA(0xFFFFFF, 0x14),
	theme.ColorNameForeground:          ink(0xE4E5E7),
	theme.ColorNamePlaceHolder:         ink(0x6E7075),
	theme.ColorNameDisabled:            ink(0x5A5C61),
	theme.ColorNameScrollBar:           inkA(0xFFFFFF, 0x52),
	theme.ColorNameScrollBarBackground: ink(0x26272A),
}

// tpColor answers one palette entry. ok is false for a name this theme does not
// define, which is what keeps the fallback honest: a colour we never chose is
// the built-in theme's, not a guess.
func tpColor(name fyne.ThemeColorName, variant fyne.ThemeVariant) (color.Color, bool) {
	table := tpLight
	if variant == theme.VariantDark {
		table = tpDark
	}
	entry, ok := table[name]
	if !ok {
		return nil, false
	}
	return entry.color(), true
}

// tpSizes are the only size overrides. They are deliberately few and
// deliberately timid:
//
//   - SizeNameText, SizeNamePadding: every text measurement and every
//     border/box gap hangs off them; several tests pin widths and heights that
//     were measured at their built-in values.
//   - SizeNameSplitThickness: the built-in theme answers 0 on purpose and the
//     Split container substitutes its own, so naming a value here would silently
//     change the drag handles.
//   - Row heights and the editor's metrics: the grid and the line-number gutter
//     are aligned to them.
var tpSizes = map[fyne.ThemeSizeName]float32{
	theme.SizeNameInnerPadding: 6,
	theme.SizeNameScrollBar:    10,
	theme.SizeNameInputRadius:  4,
	theme.SizeNameButtonRadius: 4,
}

// tpSize answers one size override; ok is false for everything else.
func tpSize(name fyne.ThemeSizeName) (float32, bool) {
	v, ok := tpSizes[name]
	return v, ok
}

// ---- the theme itself ----

// tpTheme is the app's theme: TablePlus's colours over fyne's built-in
// everything else. It exists because the built-in LightTheme/DarkTheme
// constructors ignore the user's preference (and are deprecated for it), while
// DefaultTheme alone has the built-in palette.
type tpTheme struct {
	base fyne.Theme
	// variant is tpFollow to follow the system, or a pinned one. It is kept on
	// the theme rather than resolved per call because the framework passes the
	// system variant in and cannot tell it from "the user pinned light".
	variant fyne.ThemeVariant
}

// newTablePlusTheme wraps the default theme in the TablePlus palette.
func newTablePlusTheme(variant fyne.ThemeVariant) *tpTheme {
	return &tpTheme{base: theme.DefaultTheme(), variant: variant}
}

// Color implements fyne.Theme.
func (t *tpTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if t.variant != tpFollow {
		variant = t.variant
	}
	// A variant that is neither light nor dark means the caller carries no
	// user preference (the test driver passes one). Answering the built-in
	// theme wholesale keeps a window from ending up half one way, half the
	// other: a mixed answer looks more broken than an unstyled one.
	if variant != theme.VariantDark && variant != theme.VariantLight {
		return t.base.Color(name, variant)
	}
	if c, ok := tpColor(name, variant); ok {
		return c
	}
	return t.base.Color(name, variant)
}

// Font implements fyne.Theme: fonts are the built-in ones.
func (t *tpTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.base.Font(style)
}

// Icon implements fyne.Theme: icons are the built-in ones.
func (t *tpTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

// Size implements fyne.Theme.
func (t *tpTheme) Size(name fyne.ThemeSizeName) float32 {
	if v, ok := tpSize(name); ok {
		return v
	}
	return t.base.Size(name)
}

// ---- named colours the widgets ask for ----

// crumbBG 是面包屑框与详情栏数值框的底色：一级「面板」灰，不是内容区的白。
func crumbBG() color.Color { return theme.Color(theme.ColorNameMenuBackground) }

// crumbBorder 是面包屑框与详情栏数值框的描边色：要的是那条 1px 实线，不是投影。
func crumbBorder() color.Color { return theme.Color(theme.ColorNameSeparator) }

// detailsBG 是右侧详情栏面板的底色。
func detailsBG() color.Color { return theme.Color(theme.ColorNameMenuBackground) }

// contentBG 是内容区（编辑器、结果网格）的底色。widget.Table 不自绘底色，
// 窗口的灰会直接透到网格里 —— 上半截白、下半截灰，所以网格要自己铺一层。
func contentBG() color.Color { return theme.Color(theme.ColorNameInputBackground) }

// themedRect is one rectangle whose colour the theme owns: the shape, and the
// colour it should be filled with.
type themedRect struct {
	rect *canvas.Rectangle
	fill func() color.Color
}

// refreshThemeColors repaints every rectangle that took its colour from a
// theme. A canvas.Rectangle keeps the colour it was created with: switching
// themes hands the app a new theme and nothing goes back to repaint shapes
// already filled from the old one, so the grid's white rectangle would survive
// untouched into the dark theme.
func (c *Client) refreshThemeColors() {
	for _, tr := range c.themedRects {
		if tr.rect == nil {
			continue
		}
		tr.rect.FillColor = tr.fill()
		tr.rect.Refresh()
	}
}

// zebraColor 是结果网格隔行条纹的颜色：前景色压到很低的 alpha 叠在底色上。
// 取前景而不是某个固定浅灰，是为了明暗主题下都只轻微偏移背景；半透明则保证
// 表格自己的选中高亮从条纹底下透出来，不会被不透明条纹盖掉。
func zebraColor() color.Color {
	fg := theme.Color(theme.ColorNameForeground)
	r, g, b, _ := fg.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 14}
}
