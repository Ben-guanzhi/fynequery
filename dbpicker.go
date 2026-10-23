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

// showOpenDatabaseDialog opens the TablePlus-style "打开数据库" sheet as a
// non-modal popup anchored under the toolbar database pill, so it never
// blocks the main window. It lists the server's databases with a search box
// and 关闭 / 新建... / 打开 actions.
func showOpenDatabaseDialog(w fyne.Window, anchor fyne.Position, sess db.Session, driver, current string, onOpen func(string)) {
	// The database list is fetched in the background: SHOW DATABASES against a
	// remote server can take seconds, and doing it here on the UI thread froze
	// the whole window on click.
	var all, filtered []string
	selected := current

	search := widget.NewEntry()
	search.SetPlaceHolder("搜索数据库...")

	var list *widget.List
	// selectedName drives the row text color: widget.List paints the blue
	// selection rectangle itself, but its default label can't turn white over
	// it, so each row is a small custom widget that recolors on selection.
	selectedName := current
	list = widget.NewList(
		func() int { return len(filtered) },
		func() fyne.CanvasObject { return newDBRow() },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			row, ok := o.(*dbRow)
			if !ok || i < 0 || i >= len(filtered) {
				return
			}
			row.setName(filtered[i])
			row.setSel(filtered[i] == selectedName)
		},
	)
	list.OnSelected = func(i widget.ListItemID) {
		if i >= 0 && i < len(filtered) {
			selected = filtered[i]
			selectedName = selected
			list.Refresh() // repaint so the row's white-on-blue text follows
		}
	}

	// apply publishes a freshly fetched list on the UI thread and preselects
	// the given name (the current database on first load, a new one on create).
	apply := func(dbs []string, preselect string) {
		all = dbs
		q := strings.ToLower(strings.TrimSpace(search.Text))
		filtered = filtered[:0]
		for _, d := range all {
			if q == "" || strings.Contains(strings.ToLower(d), q) {
				filtered = append(filtered, d)
			}
		}
		list.Refresh()
		if preselect != "" {
			for i, d := range filtered {
				if d == preselect {
					selected = d
					selectedName = d
					list.Select(i)
					break
				}
			}
		}
	}

	// load fetches the server's database list off the UI thread.
	load := func(preselect string) {
		safeGo("dbpicker", func() {
			dbs := listDatabases(sess, driver)
			safeDo(func() { apply(dbs, preselect) })
		})
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

	var pop *widget.PopUp
	closeBtn := widget.NewButton("关闭", func() { pop.Hide() })
	newBtn := widget.NewButton("新建...", func() {
		showCreateDatabaseDialog(w, sess, driver, func(name string) {
			search.SetText("")
			load(name) // re-read the list off-thread and preselect the new db
		})
	})
	openBtn := widget.NewButton("打开", func() {
		if selected != "" {
			pop.Hide()
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
	pop = widget.NewPopUp(body, w.Canvas())
	size := fyne.NewSize(420, 440)
	// Clamp the anchor so the sheet stays fully on-canvas, then drop it just
	// under the toolbar pill that opened it. Show first, then Resize: a PopUp
	// re-lays out to its content MinSize on Show, which would discard a Resize
	// done beforehand and collapse the list rows to zero width.
	cs := w.Canvas().Size()
	x, y := anchor.X, anchor.Y+28
	if x+size.Width > cs.Width {
		x = cs.Width - size.Width
	}
	if y+size.Height > cs.Height {
		y = cs.Height - size.Height
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	pop.ShowAtPosition(fyne.NewPos(x, y))
	pop.Resize(size)
	list.Refresh() // re-measure rows now that the popup has its real width
	load(current)  // fill the list off-thread; the sheet opens empty meanwhile
}

// dbCreateNameHint returns the placeholder for the new-database name entry,
// dialect-aware (SQLite treats the name as a file path).
func dbCreateNameHint(driver string) string {
	if strings.EqualFold(driver, "sqlite") {
		return "数据库文件路径，如 /path/to/app.db 或 :memory:"
	}
	return "新数据库名称"
}

// dbCreateSupportsCharset reports whether the driver accepts a charset/collation
// for CREATE DATABASE. Only MySQL/MariaDB expose these today; other engines
// ignore them (created with server defaults).
func dbCreateSupportsCharset(driver string) bool {
	switch strings.ToLower(driver) {
	case "mysql", "mariadb":
		return true
	}
	return false
}

// showCreateDatabaseDialog opens the "新建数据库" sheet. When the session cannot
// create databases (e.g. Oracle via a client session), it shows an explanatory
// message instead. On success it calls onCreated with the new database name so
// the caller can refresh its picker and/or switch to the new database.
func showCreateDatabaseDialog(w fyne.Window, sess db.Session, driver string, onCreated func(string)) {
	if sess == nil {
		return
	}
	if !db.CanCreateDatabase(sess) {
		showInfo(w, "新建数据库",
			"当前连接不支持在应用内创建数据库。\n请在 SQL 标签页执行对应的 CREATE DATABASE 语句。")
		return
	}

	name := widget.NewEntry()
	name.SetPlaceHolder(dbCreateNameHint(driver))

	var charset *widget.Select
	var collation *widget.Entry
	if dbCreateSupportsCharset(driver) {
		charset = widget.NewSelect([]string{"utf8mb4", "utf8", "latin1", "ascii", "binary", "gbk"}, nil)
		charset.SetSelected("utf8mb4")
		collation = widget.NewEntry()
		collation.SetPlaceHolder("排序规则（可选，留空用默认）")
	}

	items := []*widget.FormItem{widget.NewFormItem("名称", name)}
	if charset != nil {
		items = append(items, widget.NewFormItem("字符集", charset))
		items = append(items, widget.NewFormItem("排序规则", collation))
	}
	form := widget.NewForm(items...)

	var dlg dialog.Dialog
	cancelBtn := widget.NewButton("取消", func() { dlg.Hide() })
	createBtn := widget.NewButton("创建", func() {
		nm := strings.TrimSpace(name.Text)
		if nm == "" {
			showInfo(w, "新建数据库", "请输入数据库名称。")
			return
		}
		opts := map[string]string{}
		if charset != nil {
			opts["charset"] = charset.Selected
			if c := strings.TrimSpace(collation.Text); c != "" {
				opts["collation"] = c
			}
		}
		creator, ok := db.AsDatabaseCreator(sess)
		if !ok {
			showInfo(w, "新建数据库", "当前连接不支持在应用内创建数据库。")
			return
		}
		if err := creator.CreateDatabase(nm, opts); err != nil {
			dialog.ShowError(err, w)
			return
		}
		dlg.Hide()
		if onCreated != nil {
			onCreated(nm)
		}
	})

	body := container.NewBorder(nil, container.NewHBox(layout.NewSpacer(), cancelBtn, createBtn), nil, nil, form)
	dlg = dialog.NewCustom("新建数据库", "", body, w)
	dlg.Resize(fyne.NewSize(440, 300))
	dlg.Show()
}

// dbRow is one row of the "打开数据库" list: a storage icon + the database
// name. It is deliberately not tappable so widget.List keeps handling the tap
// and paints the blue selection rectangle; this widget only recolors its label
// white over that highlight (widget.List's default label cannot).
type dbRow struct {
	widget.BaseWidget
	name  string
	icon  *canvas.Image
	label *canvas.Text
	sel   bool
}

func newDBRow() *dbRow {
	r := &dbRow{
		icon:  canvas.NewImageFromResource(theme.StorageIcon()),
		label: canvas.NewText("template", pal().foreground),
	}
	r.icon.SetMinSize(fyne.NewSize(16, 16))
	r.icon.FillMode = canvas.ImageFillContain
	r.label.TextSize = 13
	r.ExtendBaseWidget(r)
	return r
}

func (r *dbRow) setName(s string) {
	if r.name != s {
		r.name = s
		r.Refresh()
	}
}

func (r *dbRow) setSel(b bool) {
	if r.sel != b {
		r.sel = b
		r.Refresh()
	}
}

func (r *dbRow) CreateRenderer() fyne.WidgetRenderer { return &dbRowRenderer{r: r} }

type dbRowRenderer struct {
	r *dbRow
	w float32 // last laid-out width, so Refresh can re-fit the label text
}

func (rd *dbRowRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{rd.r.icon, rd.r.label}
}

func (rd *dbRowRenderer) MinSize() fyne.Size { return fyne.NewSize(120, 28) }

func (rd *dbRowRenderer) Layout(size fyne.Size) {
	rd.w = size.Width
	rd.fitLabel()
	icSz := float32(16)
	pad := float32(8)
	rd.r.icon.Move(fyne.NewPos(pad, (size.Height-icSz)/2))
	rd.r.icon.Resize(fyne.NewSize(icSz, icSz))
	t := rd.r.label.MinSize()
	rd.r.label.Move(fyne.NewPos(pad+icSz+6, (size.Height-t.Height)/2))
}

// fitLabel truncates the database name to the row's last known width. It runs
// from both Layout and Refresh so rows that were first measured at zero width
// (before the popup is resized) still pick up their text afterwards.
func (rd *dbRowRenderer) fitLabel() {
	icSz := float32(16)
	pad := float32(8)
	x := pad + icSz + 6
	w := rd.w - x - 8
	if w < 0 {
		w = 0
	}
	rd.r.label.Text = fitCellText(rd.r.name, w, rd.r.label.TextSize, rd.r.label.TextStyle)
	rd.r.label.Refresh()
}

func (rd *dbRowRenderer) Destroy() {}

func (rd *dbRowRenderer) Refresh() {
	p := pal()
	if rd.r.sel {
		rd.r.label.Color = p.selFG // white over the blue selection
	} else {
		rd.r.label.Color = p.foreground
	}
	rd.fitLabel()
	rd.r.icon.Refresh()
}
