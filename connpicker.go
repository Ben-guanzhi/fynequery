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

	cancelBtn := widget.NewButton("取消", func() { dlg.Hide() })
	body := container.NewBorder(
		container.NewVBox(search, widget.NewSeparator()),
		container.NewHBox(layout.NewSpacer(), cancelBtn),
		nil, nil,
		list,
	)
	dlg = dialog.NewCustom("选择连接", "", body, w)
	dlg.Resize(fyne.NewSize(480, 420))
	dlg.Show()
}
