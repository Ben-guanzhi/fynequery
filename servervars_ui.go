package main

// 服务器变量查看（Navicat「变量」/ HeidiSQL Variables 面板的 Fyne 形态）：
//
// 内核可选能力 ServerVariablesReader（dbcore v0.6.0）。「更多功能」菜单入口
// ——独立窗口（低频查看操作）；列表后台拉取（safeDo 回 UI，15s 超时）；
// 过滤框（名字/值）；Value 与 Default 不同标 *（改了未生效）。
// **只读**——内核接口就没有改值，窗口不出编辑框。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	dbcore "github.com/Ben-guanzhi/dbcore"

	"fynequery/db"
)

// openServerVars 弹出变量查看窗口（「更多功能」菜单入口）。
func (ws *workspace) openServerVars() {
	vr := db.VarReaderFor(ws.liveSession())
	if vr == nil {
		showInfo(ws.window, "Server Variables",
			"当前引擎不支持服务器变量查看（需要 MySQL / PostgreSQL / SQL Server）。")
		return
	}

	status := widget.NewLabel("正在读取变量…")
	status.Truncation = fyne.TextTruncateEllipsis

	var all []dbcore.ServerVariable
	filter := widget.NewEntry()
	filter.PlaceHolder = "过滤变量名/值…"

	list := widget.NewList(
		func() int { return len(visibleVars(all, filter.Text)) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i int, o fyne.CanvasObject) {
			rows := visibleVars(all, filter.Text)
			if i < 0 || i >= len(rows) {
				return
			}
			lbl := o.(*widget.Label)
			lbl.Truncation = fyne.TextTruncateEllipsis
			sv := rows[i]
			mark := ""
			if sv.Default != "" && sv.Value != sv.Default {
				mark = "  *"
			}
			lbl.SetText(sv.Name + " = " + sv.Value + mark)
		},
	)
	detail := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	detail.Wrapping = fyne.TextWrapWord
	list.OnSelected = func(id widget.ListItemID) {
		rows := visibleVars(all, filter.Text)
		if id < 0 || id >= len(rows) {
			return
		}
		sv := rows[id]
		text := sv.Name + " = " + sv.Value
		if sv.Default != "" && sv.Value != sv.Default {
			text += "\n默认值: " + sv.Default + "（当前与默认不同）"
		}
		if sv.Range != "" {
			text += "\n范围: " + sv.Range
		}
		if sv.Desc != "" {
			text += "\n" + sv.Desc
		}
		detail.SetText(text)
	}

	refresh := func() {
		status.SetText("正在读取变量…")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			vars, err := vr.ServerVariables(ctx)
			dbcore.SortVariables(vars)
			safeDo(func() {
				all = vars
				list.Refresh()
				if err != nil {
					status.SetText("读取失败：" + err.Error())
				} else {
					status.SetText(fmt.Sprintf("%d 个变量（只读）", len(vars)))
				}
			})
		}()
	}
	filter.OnChanged = func(string) { list.Refresh() }
	refresh()

	w := container.NewBorder(
		container.NewVBox(status, filter),
		container.NewVBox(detail),
		nil, nil, list)
	win := fyne.CurrentApp().NewWindow("Server Variables")
	win.SetContent(w)
	win.Resize(fyne.NewSize(760, 500))
	win.Show()
}

// visibleVars 过滤后的变量（名字或值包含过滤词）。
func visibleVars(all []dbcore.ServerVariable, term string) []dbcore.ServerVariable {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return all
	}
	out := make([]dbcore.ServerVariable, 0, len(all)/4+1)
	for _, sv := range all {
		if strings.Contains(strings.ToLower(sv.Name), term) ||
			strings.Contains(strings.ToLower(sv.Value), term) {
			out = append(out, sv)
		}
	}
	return out
}
