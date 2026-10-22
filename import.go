package main

// "Import from URL..." on the welcome screen: paste a connection URL
// (mysql://user:pass@host:port/db, postgres://..., mssql://..., oracle://...),
// parse it into a Connection and open the prefilled connection sheet.

import (
	"errors"
	"net/url"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// showImportURLDialog prompts for a connection URL and forwards the parsed
// connection to the standard (prefilled) connection sheet.
func showImportURLDialog(w fyne.Window, onConnect func(Connection, db.Session)) {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("mysql://user:pass@host:3306/dbname")

	var dlg dialog.Dialog
	doImport := func() {
		c, ok := parseConnURL(entry.Text)
		if !ok {
			dialog.NewError(errors.New("无法解析该连接 URL，请参考下方示例的格式。"), w).Show()
			return
		}
		dlg.Hide()
		showConnectionDialog(w, c, onConnect)
	}
	entry.OnSubmitted = func(string) { doImport() }
	importBtn := widget.NewButton("Import", doImport)
	importBtn.Importance = widget.HighImportance
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })

	hint := widget.NewLabelWithStyle(
		"Examples: mysql://root@localhost:3306/shop · postgres://user:pass@host:5432/app · mssql://sa:pass@host:1433/master",
		fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	hint.Wrapping = fyne.TextWrapWord

	body := container.NewVBox(
		widget.NewLabel("Paste a connection URL:"),
		entry,
		hint,
		container.NewHBox(layout.NewSpacer(), cancelBtn, importBtn),
	)

	dlg = dialog.NewCustom("Import from URL", "", body, w)
	dlg.Resize(fyne.NewSize(500, 240))
	dlg.Show()
}

// parseConnURL turns a DSN URL into a Connection. It returns ok=false when the
// scheme is unknown or the URL has no host.
func parseConnURL(raw string) (Connection, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Connection{}, false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Connection{}, false
	}
	var code string
	switch strings.ToLower(u.Scheme) {
	case "mysql", "mysql2", "mariadb":
		code = "mysql"
	case "postgres", "postgresql", "pg":
		code = "postgres"
	case "mssql", "sqlserver", "sqlserver2019":
		code = "mssql"
	case "oracle":
		code = "oracle"
	default:
		return Connection{}, false
	}
	c := Connection{
		Driver:   code,
		Host:     u.Hostname(),
		Port:     u.Port(),
		Database: strings.TrimPrefix(u.Path, "/"),
		Group:    "Work",
		Tags:     []string{"loc"},
	}
	if u.User != nil {
		c.User = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			c.Password = pw
		}
	}
	name := dbTypeByDriver(code).Name
	if name == "" {
		name = strings.ToUpper(code)
	}
	c.Name = name + " " + c.Host
	return c, true
}
