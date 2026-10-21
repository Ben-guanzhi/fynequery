package main

// TablePlus-style light and dark themes for the whole app.
// Color values follow the design references extracted from the TablePlus
// screenshots (see design-ref/).

import (
	"image/color"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// colorTransparent is fully transparent, used to hide filler shapes.
var colorTransparent = color.RGBA{R: 0, G: 0, B: 0, A: 0}

// hex parses "#RRGGBB" or "#RRGGBBAA" into an RGBA color.
func hex(s string) color.RGBA {
	s = s[1:]
	r, _ := strconv.ParseUint(s[0:2], 16, 8)
	g, _ := strconv.ParseUint(s[2:4], 16, 8)
	b, _ := strconv.ParseUint(s[4:6], 16, 8)
	a := uint64(255)
	if len(s) >= 8 {
		a, _ = strconv.ParseUint(s[6:8], 16, 8)
	}
	return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(a)}
}

// Palette of hand-tuned colors for chrome we draw ourselves (status bar,
// badges, tabs...). Fyne theme colors live in tablePlusTheme below.
// Dark values are measured from a live TablePlus Windows dark window
// (design-ref/tp-live-workspace.png); light values keep the earlier
// screenshot-calibrated palette.
type palette struct {
	// chrome
	bg            color.RGBA // window background
	sidebarBG     color.RGBA // left sidebar / welcome left panel
	toolbarBG     color.RGBA
	contentBG     color.RGBA
	stripBG       color.RGBA // top tab-strip base (TablePlus dark: #626262)
	tabBG         color.RGBA // inactive tab chip
	activeTabBG   color.RGBA // active tab chip
	inactiveTabFG color.RGBA
	activeTabFG   color.RGBA
	tabUnderline  color.RGBA
	line          color.RGBA // hairline borders
	dividerLine   color.RGBA // panel divider / scrollbar accent (TablePlus custom #FA5151)
	statusBG      color.RGBA
	statusSideBG  color.RGBA // status segment under the sidebar
	statusFG      color.RGBA
	statusLink    color.RGBA
	treeSel       color.RGBA // sidebar tree selection (TablePlus teal-blue)
	pkHeader      color.RGBA // primary-key column header highlight (#FFDF9D)
	cellInt       color.RGBA // int / bigint value color (blue)
	cellFloat     color.RGBA // decimal / float value color (mint)
	cellDate      color.RGBA // date / datetime value color (gold)
	cellStr       color.RGBA // varchar / text value color (orange)
	cellDefault   color.RGBA // everything else (white in dark)
	// grid
	gridHeaderBG    color.RGBA
	gridHeaderFG    color.RGBA
	gridRowAltBG    color.RGBA
	gridRowHoverBG  color.RGBA
	gridRowSelectBG color.RGBA
	gridCellFG      color.RGBA
	gridNullFG      color.RGBA
	gridLine        color.RGBA // 1px row separator line
	gutterBG        color.RGBA // row-number gutter background
	cellEdited      color.RGBA // an unsaved (edited) cell value
	rowDeletedBG    color.RGBA // background of a row marked for deletion
	// server ribbon (green info bar at the top of the workspace)
	ribbonBG   color.RGBA
	ribbonFG   color.RGBA
	ribbonInfo color.RGBA // orange info dot at the ribbon's right edge
	// toolbar chip groups (rounded white containers around icon buttons)
	groupBG     color.RGBA
	groupBorder color.RGBA
	// query result status dot (bottom "● Query result" line)
	okDot color.RGBA
	// breadcrumb nav bar + details pane
	crumbBG     color.RGBA // rounded table-name field background
	crumbBorder color.RGBA
	detailBG    color.RGBA // right details sidebar background
	detailLine  color.RGBA // property row separator
	metaKeyFG   color.RGBA // property key text (dimmed)
	metaValFG   color.RGBA // property value text
	// misc
	foreground      color.RGBA
	accent          color.RGBA
	accentDark      color.RGBA
	hover           color.RGBA
	segmentBG       color.RGBA
	segmentActive   color.RGBA
	segmentFG       color.RGBA
	segmentActiveFG color.RGBA
	welcomePanelBG  color.RGBA
	welcomeFG       color.RGBA
}

var lightPalette = palette{
	bg:              hex("#FFFFFF"),
	sidebarBG:       hex("#F6F6F6"),
	toolbarBG:       hex("#F5F5F5"),
	contentBG:       hex("#FFFFFF"),
	stripBG:         hex("#F6F6F6"),
	tabBG:           hex("#F6F6F6"),
	activeTabBG:     hex("#FFFFFF"),
	inactiveTabFG:   hex("#8A8A8A"),
	activeTabFG:     hex("#2B2B2B"),
	tabUnderline:    hex("#DADADA"),
	line:            hex("#E6E6E6"),
	dividerLine:     hex("#E3E3E3"),
	statusBG:        hex("#F6F6F6"),
	statusSideBG:    hex("#F6F6F6"),
	statusFG:        hex("#6B6B6B"),
	statusLink:      hex("#2F7BD9"),
	treeSel:         hex("#E8EFF7"),
	pkHeader:        hex("#8A6D00"),
	cellInt:         hex("#1F6FEB"),
	cellFloat:       hex("#00856C"),
	cellDate:        hex("#9A6700"),
	cellStr:         hex("#C05A1F"),
	cellDefault:     hex("#333333"),
	gridHeaderBG:    hex("#FFFFFF"),
	gridHeaderFG:    hex("#444444"),
	gridRowAltBG:    hex("#FBFBFB"),
	gridRowHoverBG:  hex("#F3F7FC"),
	gridRowSelectBG: hex("#E8EFF7"),
	gridCellFG:      hex("#333333"),
	gridNullFG:      hex("#A0A0A0"),
	gridLine:        hex("#E9E9E9"),
	gutterBG:        hex("#F7F7F7"),
	cellEdited:      hex("#C05621"),
	rowDeletedBG:    hex("#FDECEA"),
	ribbonBG:        hex("#D9EBC9"),
	ribbonFG:        hex("#2F5B36"),
	ribbonInfo:      hex("#F0A93B"),
	groupBG:         hex("#FFFFFF"),
	groupBorder:     hex("#E7E7E7"),
	okDot:           hex("#2FB344"),
	crumbBG:         hex("#FFFFFF"),
	crumbBorder:     hex("#DCDCDC"),
	detailBG:        hex("#F7F7F7"),
	detailLine:      hex("#E8E8E8"),
	metaKeyFG:       hex("#8A8A8A"),
	metaValFG:       hex("#333333"),
	foreground:      hex("#333333"),
	accent:          hex("#2F7BD9"),
	accentDark:      hex("#1E5AA8"),
	hover:           hex("#EFEFEF"),
	segmentBG:       hex("#ECECEC"),
	segmentActive:   hex("#FFFFFF"),
	segmentFG:       hex("#6B6B6B"),
	segmentActiveFG: hex("#2B2B2B"),
	welcomePanelBG:  hex("#F5F5F5"),
	welcomeFG:       hex("#6B6B6B"),
}

var darkPalette = palette{
	bg:              hex("#1E1E1E"),
	sidebarBG:       hex("#232323"),
	toolbarBG:       hex("#232323"),
	contentBG:       hex("#1E1E1E"),
	stripBG:         hex("#232323"),
	tabBG:           hex("#262626"),
	activeTabBG:     hex("#363636"),
	inactiveTabFG:   hex("#A9A9A9"),
	activeTabFG:     hex("#FFFFFF"),
	tabUnderline:    hex("#4A4A4A"),
	line:            hex("#333333"),
	dividerLine:     hex("#3A3A3A"),
	statusBG:        hex("#232323"),
	statusSideBG:    hex("#232323"),
	statusFG:        hex("#A9A9A9"),
	statusLink:      hex("#79BEFF"),
	treeSel:         hex("#3A5B6E"),
	pkHeader:        hex("#FFDF9D"),
	cellInt:         hex("#79BEFF"),
	cellFloat:       hex("#BEFFDF"),
	cellDate:        hex("#FFDF9D"),
	cellStr:         hex("#FFBE79"),
	cellDefault:     hex("#FFFFFF"),
	gridHeaderBG:    hex("#2B2B2B"),
	gridHeaderFG:    hex("#FFFFFF"),
	gridRowAltBG:    hex("#242424"),
	gridRowHoverBG:  hex("#2A2A2A"),
	gridRowSelectBG: hex("#3A5B6E"),
	gridCellFG:      hex("#FFFFFF"),
	gridNullFG:      hex("#7A7A7A"),
	gridLine:        hex("#333333"),
	gutterBG:        hex("#232323"),
	cellEdited:      hex("#E5A15C"),
	rowDeletedBG:    hex("#4A2A2A"),
	ribbonBG:        hex("#2E4632"),
	ribbonFG:        hex("#CFE9CC"),
	ribbonInfo:      hex("#F0A93B"),
	groupBG:         hex("#2B2B2B"),
	groupBorder:     hex("#3A3A3A"),
	okDot:           hex("#3DD68C"),
	crumbBG:         hex("#2D2D2D"),
	crumbBorder:     hex("#3C3C3C"),
	detailBG:        hex("#232323"),
	detailLine:      hex("#333333"),
	metaKeyFG:       hex("#8A8A8A"),
	metaValFG:       hex("#DADADA"),
	foreground:      hex("#CCCCCC"),
	accent:          hex("#007F3D"),
	accentDark:      hex("#0A6938"),
	hover:           hex("#333333"),
	segmentBG:       hex("#333333"),
	segmentActive:   hex("#3D3D3D"),
	segmentFG:       hex("#A9A9A9"),
	segmentActiveFG: hex("#FFFFFF"),
	welcomePanelBG:  hex("#1B1B1B"),
	welcomeFG:       hex("#A9A9A9"),
}

// pal returns the palette for the current UI mode.
var darkMode bool

func pal() *palette {
	if darkMode {
		return &darkPalette
	}
	return &lightPalette
}

type tablePlusTheme struct {
	base fyne.Theme
}

func (t *tablePlusTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	p := pal()
	variant := theme.VariantLight
	if darkMode {
		variant = theme.VariantDark
	}
	switch name {
	case theme.ColorNameBackground:
		return p.contentBG
	case theme.ColorNameButton:
		return p.toolbarBG
	case theme.ColorNameDisabled:
		return hex("#AAAAAA")
	case theme.ColorNameDisabledButton:
		if darkMode {
			return hex("#333333")
		}
		return hex("#E8E8E8")
	case theme.ColorNameError:
		return hex("#E53935")
	case theme.ColorNameFocus:
		return p.accent
	case theme.ColorNameForeground:
		return p.foreground
	case theme.ColorNameHover:
		return p.hover
	case theme.ColorNameHeaderBackground:
		return p.toolbarBG
	case theme.ColorNameInputBackground:
		if darkMode {
			return hex("#333333")
		}
		return hex("#FFFFFF")
	case theme.ColorNameInputBorder:
		if darkMode {
			return hex("#333333")
		}
		return hex("#D9D9D9")
	case theme.ColorNameMenuBackground:
		if darkMode {
			return hex("#252526")
		}
		return hex("#FCFCFC")
	case theme.ColorNameOverlayBackground:
		// Dialog/popup box background — must track the theme so dialogs match
		// the window (Fyne forces this opaque for the dialog box).
		if darkMode {
			return hex("#2D2D2D")
		}
		return hex("#F7F7F7")
	case theme.ColorNamePlaceHolder:
		if darkMode {
			return hex("#7A7A7A")
		}
		return hex("#9A9A9A")
	case theme.ColorNamePressed:
		if darkMode {
			return hex("#094771")
		}
		return hex("#DDEBF7")
	case theme.ColorNamePrimary:
		return p.accent
	case theme.ColorNameScrollBar:
		return p.line
	case theme.ColorNameSelection:
		return p.treeSel
	case theme.ColorNameSeparator:
		return p.line
	case theme.ColorNameShadow:
		// Fyne paints split dividers (and popup shadows) with the shadow
		// colour; a flat hairline matches the redesign better than a dark bar.
		return p.line
	case theme.ColorNameSuccess:
		return hex("#4CAF50")
	case theme.ColorNameWarning:
		return hex("#F5A623")
	}
	return t.base.Color(name, variant)
}

func (t *tablePlusTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.base.Font(style)
}

func (t *tablePlusTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

func (t *tablePlusTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 13
	case theme.SizeNameHeadingText:
		return 26
	case theme.SizeNameSubHeadingText:
		return 16
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameInputBorder:
		return 1
	case theme.SizeNameInputRadius:
		return 7
	case theme.SizeNameSeparatorThickness:
		return 1
	case theme.SizeNameSplitThickness:
		return 1
	case theme.SizeNameScrollBar:
		return 10
	case theme.SizeNamePadding:
		return 5
	case theme.SizeNameInnerPadding:
		return 7
	}
	return t.base.Size(name)
}
