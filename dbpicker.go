package main

// Database picker ("打开数据库"): lists the databases on the connected server
// and lets the workspace switch to the chosen one, mirroring TablePlus's
// database-icon / Ctrl+K flow. Listing reuses the ordinary Query path
// (SHOW DATABASES on MySQL), so no driver-protocol change is needed.

import (
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// isSystemSchema reports whether a database name is a built-in schema that
// TablePlus hides from the picker.
func isSystemSchema(name string) bool {
	switch strings.ToLower(name) {
	case "information_schema", "mysql", "performance_schema", "sys",
		"pg_catalog", "template0", "template1":
		return true
	}
	return false
}

// listDatabases returns the user databases on the server, dialect-aware.
func listDatabases(sess db.Session, driver string) []string {
	if sess == nil {
		return nil
	}
	var q string
	switch driver {
	case "postgres", "postgresql":
		q = "SELECT datname FROM pg_database WHERE datistemplate = false ORDER BY datname"
	case "mssql":
		q = "SELECT name FROM sys.databases ORDER BY name"
	default: // mysql / mariadb
		q = "SHOW DATABASES"
	}
	res, err := sess.Query(q, 5000)
	if err != nil {
		return nil
	}
	var out []string
	for _, row := range res.Rows {
		if len(row) > 0 && row[0] != nil {
			name := *row[0]
			if isSystemSchema(name) {
				continue
			}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// showOpenDatabaseDialog opens the TablePlus-style "打开数据库" sheet: a
// searchable list of the server's databases with 关闭 / 新建... / 打开.
func showOpenDatabaseDialog(w fyne.Window, sess db.Session, driver, current string, onOpen func(string)) {
	all := listDatabases(sess, driver)
	filtered := append([]string(nil), all...)
	selected := current

	search := widget.NewEntry()
	search.SetPlaceHolder("搜索数据库...")

	var list *widget.List
	list = widget.NewList(
		func() int { return len(filtered) },
		func() fyne.CanvasObject {
			icon := canvas.NewImageFromResource(theme.StorageIcon())
			icon.SetMinSize(fyne.NewSize(16, 16))
			icon.FillMode = canvas.ImageFillContain
			label := widget.NewLabel("template")
			label.Truncation = fyne.TextTruncateEllipsis
			// Border layout: icon on the left (fixed), label in the center so
			// it fills the row width and shows the full database name.
			return container.NewBorder(nil, nil, icon, nil, label)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			box := o.(*fyne.Container)
			for _, obj := range box.Objects {
				if l, ok := obj.(*widget.Label); ok {
					l.SetText(filtered[i])
				}
			}
		},
	)
	list.OnSelected = func(i widget.ListItemID) {
		if i >= 0 && i < len(filtered) {
			selected = filtered[i]
		}
	}
	for i, d := range all { // preselect the current database
		if d == current {
			list.Select(i)
			break
		}
	}

	search.OnChanged = func(q string) {
		q = strings.ToLower(strings.TrimSpace(q))
		filtered = filtered[:0]
		for _, d := range all {
			if q == "" || strings.Contains(strings.ToLower(d), q) {
				filtered = append(filtered, d)
			}
		}
		list.Refresh()
	}

	var dlg dialog.Dialog
	closeBtn := widget.NewButton("关闭", func() { dlg.Hide() })
	newBtn := widget.NewButton("新建...", func() {})
	openBtn := widget.NewButton("打开", func() {
		if selected != "" {
			dlg.Hide()
			if onOpen != nil {
				onOpen(selected)
			}
		}
	})

	body := container.NewBorder(
		container.NewVBox(search, widget.NewSeparator()),
		container.NewHBox(layout.NewSpacer(), closeBtn, newBtn, openBtn),
		nil, nil,
		list,
	)
	dlg = dialog.NewCustom("打开数据库", "", body, w)
	dlg.Resize(fyne.NewSize(460, 480))
	dlg.Show()
}
