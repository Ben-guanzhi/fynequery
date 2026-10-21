package main

// Theme import/export as JSON (TablePlus "Custom themes / Git themes"
// lightweight version): the active palette serialises to a name→hex map,
// which can be shared or restored later.

import (
	"encoding/json"
	"fmt"
	"image/color"
	"path/filepath"
	"reflect"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
)

// paletteField reads colour field i of a palette. The fields are
// unexported, so reflection via the type (not via Value.Field().Interface,
// which panics on unexported fields) is used.
func paletteField(p *palette, i int) color.RGBA {
	rv := reflect.ValueOf(p).Elem().Field(i)
	// The fields are unexported, so we reach them through a pointer to their
	// address (reflect.NewAt) rather than via .Interface(), which panics on
	// unexported fields.
	return reflect.NewAt(reflect.TypeFor[color.RGBA](), rv.Addr().UnsafePointer()).Elem().Interface().(color.RGBA)
}

// setPaletteField writes colour c into palette field i (same unexported
// escape hatch).
func setPaletteField(p *palette, i int, c color.RGBA) {
	rv := reflect.ValueOf(p).Elem().Field(i)
	reflect.NewAt(reflect.TypeFor[color.RGBA](), rv.Addr().UnsafePointer()).Elem().Set(reflect.ValueOf(c))
}

// paletteToMap serialises every colour.RGBA field of a palette into a
// lowercase-field-name → "#RRGGBB" map.
func paletteToMap(p *palette) map[string]string {
	t := reflect.TypeOf(p).Elem()
	out := make(map[string]string, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Type != reflect.TypeFor[color.RGBA]() {
			continue
		}
		c := paletteField(p, i)
		out[strings.ToLower(t.Field(i).Name)] = fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
	}
	return out
}

// applyPaletteMap sets the named palette fields from a JSON map, returning
// how many fields were applied.
func applyPaletteMap(m map[string]string, target *palette) int {
	t := reflect.TypeOf(target).Elem()
	n := 0
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Type != reflect.TypeFor[color.RGBA]() {
			continue
		}
		name := strings.ToLower(t.Field(i).Name)
		if h, ok := m[name]; ok {
			if !strings.HasPrefix(h, "#") {
				h = "#" + h
			}
			setPaletteField(target, i, hex(h))
			n++
		}
	}
	return n
}

// exportThemeJSON offers a save dialog and writes the current palette.
func exportThemeJSON(w fyne.Window) {
	target := pal() // whichever palette is live
	data, err := json.MarshalIndent(paletteToMap(target), "", "  ")
	if err != nil {
		showInfo(w, "导出主题", err.Error())
		return
	}
	suggested := filepath.Join(configDir(), "theme.json")
	start := storage.NewFileURI(suggested)
	dialog.ShowFileSave(func(f fyne.URIWriteCloser, err error) {
		if err != nil || f == nil {
			return
		}
		if _, werr := f.Write(data); werr != nil {
			f.Close()
			showInfo(w, "导出主题", werr.Error())
			return
		}
		path := start.Path()
		if f.URI() != nil {
			path = f.URI().Path()
		}
		f.Close()
		showInfo(w, "导出主题", "主题已导出：\n"+path)
	}, w)
}

// importThemeJSON offers an open dialog and applies the file to the live
// palette, refreshing the whole window.
func importThemeJSON(w fyne.Window) {
	dialog.ShowFileOpen(func(f fyne.URIReadCloser, err error) {
		if err != nil || f == nil {
			return
		}
		defer f.Close()
		m := map[string]string{}
		if err := json.NewDecoder(f).Decode(&m); err != nil {
			showInfo(w, "导入主题", "不是有效的主题 JSON：\n"+err.Error())
			return
		}
		n := applyPaletteMap(m, pal())
		if n == 0 {
			showInfo(w, "导入主题", "文件中没有可识别的颜色项。")
			return
		}
		applyTheme(w)
		showInfo(w, "导入主题", fmt.Sprintf("已应用 %d 项颜色（模式：%s）。", n, activeThemeName()))
	}, w)
}

func activeThemeName() string {
	if darkMode {
		return "dark"
	}
	return "light"
}
