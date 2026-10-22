package main

// Connection picker ("选择连接"): a searchable sheet listing the saved
// connections, mirroring the welcome list. Opened from the workspace
// toolbar by clicking the connection breadcrumb — picking one dials it and
// hands the live session to onPick (the workspace then opens a new workspace
// window for it, like TablePlus switching connections).

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// showConnectionPicker opens the "选择连接" sheet. Picking a connection
// dials it in the background (progress dialog) and calls onPick with the
// live session; dial errors surface in an error dialog.
func showConnectionPicker(w fyne.Window, conns []Connection, onPick func(Connection, db.Session)) {
	filtered := append([]Connection(nil), conns...)
	selIdx := -1

	var dlg dialog.Dialog
	pick := func(c Connection) {
		dlg.Hide()
		progress := dialog.NewProgressInfinite("Connecting",
			fmt.Sprintf("Connecting to %s (%s)...", c.Name, c.Host), w)
		progress.Show()
		safeGo("connpicker.dial", func() {
			sess, err := dialConn(c)
			safeDo(func() {
				progress.Hide()
				if err != nil {
					dialog.NewError(fmt.Errorf("%s: %v",
						dbTypeByDriver(c.Driver).Name, err), w).Show()
					return
				}
				if onPick != nil {
					onPick(c, sess)
				}
			})
		})
	}

	noMatch := widget.NewLabelWithStyle("没有匹配的连接", fyne.TextAlignCenter, fyne.TextStyle{})
	noMatch.Importance = widget.LowImportance
	noMatch.Hide()

	openBtn := widget.NewButton("打开", func() {
		if selIdx >= 0 && selIdx < len(filtered) {
			pick(filtered[selIdx])
		}
	})
	openBtn.Importance = widget.HighImportance
	openBtn.Disable()

	var list *widget.List
	search := widget.NewEntry()
	search.SetPlaceHolder("搜索连接...")
	search.OnChanged = func(q string) {
		q = strings.ToLower(strings.TrimSpace(q))
		filtered = filtered[:0]
		for _, c := range conns {
			if q == "" ||
				strings.Contains(strings.ToLower(c.Name), q) ||
				strings.Contains(strings.ToLower(c.Host), q) {
				filtered = append(filtered, c)
			}
		}
		selIdx = -1
		openBtn.Disable()
		if len(filtered) == 0 {
			noMatch.Show()
		} else {
			noMatch.Hide()
		}
		list.Refresh()
	}

	list = widget.NewList(
		func() int { return len(filtered) },
		func() fyne.CanvasObject {
			row := newConnRow(nil)
			row.onTap = pick
			return row
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			o.(*connRow).fill(filtered[i])
		},
	)
	// Keyboard: arrows move the highlight (selection); 打开 (or Enter on it)
	// commits, so arrowing never dials by accident.
	list.OnSelected = func(i widget.ListItemID) {
		selIdx = int(i)
		if selIdx >= 0 && selIdx < len(filtered) {
			openBtn.Enable()
		} else {
			openBtn.Disable()
		}
	}
	list.OnUnselected = func(widget.ListItemID) {
		selIdx = -1
		openBtn.Disable()
	}
	listArea := container.NewStack(list, container.NewCenter(noMatch))

	cancelBtn := widget.NewButton("取消", func() { dlg.Hide() })
	body := container.NewBorder(
		container.NewVBox(search, widget.NewSeparator()),
		container.NewHBox(layout.NewSpacer(), cancelBtn, openBtn),
		nil, nil,
		listArea,
	)
	dlg = dialog.NewCustom("选择连接", "", body, w)
	dlg.Resize(fyne.NewSize(480, 420))
	dlg.Show()
}
