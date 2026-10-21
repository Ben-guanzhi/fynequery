package main

// Welcome / connection-picker page modeled on the TablePlus welcome screen:
// a branding panel on the left and the grouped saved-connection list on the
// right. Picking a connection dials the database for real; failures surface
// the driver error in a dialog.

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

type welcomePage struct {
	window  fyne.Window
	onPick  func(Connection, db.Session)
	conns   []Connection
	listBox *fyne.Container
}

func newWelcomePage(w fyne.Window, onPick func(Connection, db.Session)) *welcomePage {
	return &welcomePage{window: w, onPick: onPick}
}

func (p *welcomePage) build() fyne.CanvasObject {
	// ---- left branding panel ----
	logo := canvas.NewImageFromResource(logoResource)
	logo.SetMinSize(fyne.NewSize(104, 92))

	welcome1 := canvas.NewText("Welcome to", pal().welcomeFG)
	welcome1.TextSize = 22
	welcome2 := canvas.NewText("TablePro", pal().foreground)
	welcome2.TextSize = 30
	welcome2.TextStyle.Bold = true

	themeBtn := widget.NewButton("Toggle Dark Theme", func() {
		darkMode = !darkMode
		applyTheme(p.window)
	})
	exportTheme := widget.NewButton("Export Theme", func() { exportThemeJSON(p.window) })
	importTheme := widget.NewButton("Import Theme", func() { importThemeJSON(p.window) })

	left := container.NewVBox(
		layout.NewSpacer(),
		container.NewHBox(layout.NewSpacer(), logo, layout.NewSpacer()),
		container.NewHBox(layout.NewSpacer(), welcome1, layout.NewSpacer()),
		container.NewHBox(layout.NewSpacer(), welcome2, layout.NewSpacer()),
		widget.NewLabel(""),
		container.NewHBox(layout.NewSpacer(), themeBtn, layout.NewSpacer()),
		container.NewHBox(layout.NewSpacer(),
			container.NewHBox(exportTheme, importTheme), layout.NewSpacer()),
		widget.NewLabel("Version 1.0.0 · A native tool for relational databases"),
		layout.NewSpacer(),
		container.NewHBox(
			layout.NewSpacer(),
			newWelcomeAction(theme.DownloadIcon(), "Backup", p.backup),
			spacerW(18),
			newWelcomeAction(theme.UploadIcon(), "Restore", p.restore),
			spacerW(18),
			newWelcomeAction(theme.DocumentIcon(), "License", p.license),
			layout.NewSpacer(),
		),
	)
	leftBg := newPanelBackground(container.NewPadded(left), func() color.Color { return pal().welcomePanelBG })

	// ---- right connection list ----
	p.listBox = container.NewVBox()
	p.reload()

	newLink := widget.NewHyperlink("Create a new connection...", nil)
	newLink.OnTapped = func() {
		showNewConnectionDialog(p.window, func(c Connection, s db.Session) {
			p.reload()
			if p.onPick != nil {
				p.onPick(c, s)
			}
		})
	}
	importLink := widget.NewHyperlink("Import from URL...", nil)
	importLink.OnTapped = func() {
		showImportURLDialog(p.window, func(c Connection, s db.Session) {
			p.reload()
			if p.onPick != nil {
				p.onPick(c, s)
			}
		})
	}

	right := container.NewBorder(
		container.NewPadded(widget.NewLabelWithStyle("Connections", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})),
		container.NewVBox(
			widget.NewSeparator(),
			container.NewPadded(container.NewHBox(newLink, widget.NewLabel("  "), importLink)),
		),
		nil, nil,
		container.NewScroll(container.NewPadded(p.listBox)),
	)

	split := container.NewHSplit(leftBg, right)
	split.SetOffset(0.36)
	return split
}

// reload re-reads connections.json and rebuilds the grouped list.
func (p *welcomePage) reload() {
	conns, err := loadConnections()
	if err != nil {
		showInfo(p.window, "Connections", "Could not read saved connections:\n"+err.Error())
	}
	p.conns = conns

	p.listBox.Objects = nil
	if len(conns) == 0 {
		hint := canvas.NewText("No connections yet.\nClick \"Create a new connection...\" below.", pal().welcomeFG)
		hint.TextSize = 13
		p.listBox.Objects = append(p.listBox.Objects, container.NewPadded(hint))
		p.listBox.Refresh()
		return
	}
	for _, g := range groupConnections(conns) {
		rows := container.NewVBox()
		for _, c := range g.Conns {
			row := newConnRow(p)
			row.fill(c)
			rows.Objects = append(rows.Objects, row)
		}
		hdr := newGroupHeader(g.Name, true, func(open bool) {
			if open {
				rows.Show()
			} else {
				rows.Hide()
			}
			rows.Refresh()
		})
		p.listBox.Objects = append(p.listBox.Objects,
			container.NewPadded(hdr), rows, widget.NewSeparator())
	}
	p.listBox.Refresh()
}

// connect dials c in the background with a progress dialog and hands the
// live session to onPick; driver errors surface in an error dialog.
func (p *welcomePage) connect(c Connection) {
	progress := dialog.NewProgressInfinite("Connecting", fmt.Sprintf("Connecting to %s (%s)...", c.Name, c.Host), p.window)
	progress.Show()
	safeGo("welcome.connect", func() {
		sess, err := dialConn(c)
		safeDo(func() {
			progress.Hide()
			if err != nil {
				dialog.NewError(fmt.Errorf("%s: %v", dbTypeByDriver(c.Driver).Name, err), p.window).Show()
				return
			}
			if p.onPick != nil {
				p.onPick(c, sess)
			}
		})
	})
}

// ---- rows ------------------------------------------------------------------

// connRow is one row of the connection list.
type connRow struct {
	widget.BaseWidget
	page  *welcomePage
	conn  Connection
	onTap func(Connection) // optional override (connection picker); nil = page.connect
	badge *fyne.Container
	dot   *canvas.Circle
	name  *canvas.Text
	tags  *fyne.Container
	host  *canvas.Text
}

func newConnRow(page *welcomePage) *connRow {
	r := &connRow{
		page:  page,
		badge: container.NewStack(),
		name:  canvas.NewText("", pal().foreground),
		tags:  container.NewHBox(),
		host:  canvas.NewText("", pal().welcomeFG),
	}
	r.name.TextSize = 13
	r.name.TextStyle.Bold = true
	r.host.TextSize = 11
	r.dot = canvas.NewCircle(color.Transparent)
	r.dot.Hide()
	r.ExtendBaseWidget(r)
	return r
}

func (r *connRow) fill(c Connection) {
	r.conn = c
	t := dbTypeByDriver(c.Driver)
	r.badge.Objects = []fyne.CanvasObject{typeBadge(t, 30)}
	r.badge.Refresh()
	r.name.Text = c.Name
	r.name.Color = pal().foreground
	r.tags.Objects = nil
	for _, tg := range tagsFor(c) {
		r.tags.Objects = append(r.tags.Objects, tagChip(tg))
	}
	r.tags.Refresh()
	r.host.Text = connHostLine(c)
	r.host.Color = pal().welcomeFG
	if h := c.StatusColorHex(); h != "" {
		r.dot.FillColor = hex(h)
		r.dot.Show()
	} else {
		r.dot.FillColor = color.Transparent
		r.dot.Hide()
	}
	r.Refresh()
}

// Tapped connects; TappedSecondary (right click) offers delete.
func (r *connRow) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap(r.conn)
		return
	}
	if r.page != nil {
		r.page.connect(r.conn)
	}
}

func (r *connRow) TappedSecondary(*fyne.PointEvent) {
	if r.page == nil {
		return
	}
	dialog.NewConfirm("Delete Connection",
		fmt.Sprintf("Delete connection %q?\n\nThe database itself is not touched.", r.conn.Name),
		func(ok bool) {
			if !ok {
				return
			}
			if err := deleteConnection(r.conn.Name); err != nil {
				dialog.NewError(err, r.page.window).Show()
				return
			}
			r.page.reload()
		}, r.page.window).Show()
}

func (r *connRow) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (r *connRow) CreateRenderer() fyne.WidgetRenderer {
	// badge + status dot sit together, vertically centred against the
	// name/host text block.
	head := container.NewCenter(
		container.NewHBox(r.dot, container.NewPadded(r.badge)))
	row := container.NewHBox(container.NewPadded(head),
		container.NewVBox(container.NewHBox(r.name, r.tags), r.host))
	return widget.NewSimpleRenderer(row)
}

// MinSize guarantees two full text lines per row (Fyne lists clip otherwise).
func (r *connRow) MinSize() fyne.Size {
	base := r.BaseWidget.MinSize()
	if base.Height < 52 {
		base.Height = 52
	}
	return base
}

// showInfo is a small helper for informational dialogs.
func showInfo(w fyne.Window, title, text string) {
	d := dialog.NewInformation(title, text, w)
	d.Show()
}

// ---- footer actions --------------------------------------------------------

func (p *welcomePage) backup() {
	conns, err := loadConnections()
	if err != nil || len(conns) == 0 {
		showInfo(p.window, "Backup", "No saved connections to back up.")
		return
	}
	showConnectionPicker(p.window, conns, func(c Connection, s db.Session) {
		showBackupDialog(p.window, c, s)
	})
}

func (p *welcomePage) restore() {
	conns, err := loadConnections()
	if err != nil || len(conns) == 0 {
		showInfo(p.window, "Restore", "No saved connections to restore into.")
		return
	}
	showConnectionPicker(p.window, conns, func(c Connection, s db.Session) {
		showRestoreDialog(p.window, c, s)
	})
}

func (p *welcomePage) license() {
	showLicenseDialog(p.window)
}

// welcomeAction is a large circular icon with a caption underneath, used for
// the Backup / Restore / License shortcuts on the welcome screen.
type welcomeAction struct {
	widget.BaseWidget
	icon  fyne.Resource
	label string
	onTap func()

	circle *canvas.Circle
	img    *canvas.Image
	hover  bool
}

func newWelcomeAction(icon fyne.Resource, label string, onTap func()) *welcomeAction {
	a := &welcomeAction{icon: icon, label: label, onTap: onTap}
	a.circle = canvas.NewCircle(pal().segmentBG)
	a.img = canvas.NewImageFromResource(icon)
	a.img.FillMode = canvas.ImageFillContain
	a.ExtendBaseWidget(a)
	return a
}

func (a *welcomeAction) Tapped(*fyne.PointEvent) {
	if a.onTap != nil {
		a.onTap()
	}
}

func (a *welcomeAction) MouseIn(*desktop.MouseEvent) {
	a.hover = true
	a.Refresh()
}

func (a *welcomeAction) MouseMoved(*desktop.MouseEvent) {}

func (a *welcomeAction) MouseOut() {
	a.hover = false
	a.Refresh()
}

func (a *welcomeAction) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (a *welcomeAction) CreateRenderer() fyne.WidgetRenderer {
	img := container.NewCenter(container.NewGridWrap(fyne.NewSize(22, 22), a.img))
	stack := container.NewStack(a.circle, img)
	iconBox := container.NewGridWrap(fyne.NewSize(56, 56), stack)
	cap := canvas.NewText(a.label, pal().welcomeFG)
	cap.TextSize = 12
	cap.Alignment = fyne.TextAlignCenter
	body := container.NewVBox(container.NewCenter(iconBox), container.NewCenter(cap))
	return widget.NewSimpleRenderer(body)
}

func (a *welcomeAction) Refresh() {
	a.circle.FillColor = pal().segmentBG
	if a.hover {
		a.circle.FillColor = pal().hover
	}
	a.BaseWidget.Refresh()
}
