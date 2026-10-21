package main

import (
	"context"
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

	"tablepro-fyne/internal/db"
)

// engineColor is the brand-ish tint each engine gets in the sidebar avatars,
// so a MySQL and a Postgres connection read as different at a glance - the way
// TablePlus colours its connection list.
var engineColor = map[string]color.Color{
	db.DbTypeMySQL:     color.NRGBA{0xE9, 0x7B, 0x39, 0xFF}, // MySQL orange
	db.DbTypePostgres:  color.NRGBA{0x4C, 0x8F, 0xDB, 0xFF}, // PostgreSQL blue
	db.DbTypeSQLite:    color.NRGBA{0x1B, 0x7F, 0x79, 0xFF}, // SQLite teal
	db.DbTypeSQLServer: color.NRGBA{0xC0, 0x39, 0x2B, 0xFF}, // SQL Server red
	db.DbTypeOracle:    color.NRGBA{0xE2, 0x43, 0x29, 0xFF}, // Oracle red
}

// engineShort is the two-letter code printed on each avatar.
var engineShort = map[string]string{
	db.DbTypeMySQL:     "My",
	db.DbTypePostgres:  "PG",
	db.DbTypeSQLite:    "Lt",
	db.DbTypeSQLServer: "MS",
	db.DbTypeOracle:    "Or",
}

// fixedSize forces a minimum size on a child - used to keep the round avatar a
// constant 26px regardless of its (zero-min) canvas shapes.
type fixedSize struct {
	fyne.CanvasObject
	w, h float32
}

func (f *fixedSize) MinSize() fyne.Size { return fyne.NewSize(f.w, f.h) }

// engineAvatar is the small round badge TablePlus puts by each connection:
// a filled circle in the engine's colour with its two-letter code centred.
func engineAvatar(typ string) fyne.CanvasObject {
	col, ok := engineColor[typ]
	if !ok {
		col = color.NRGBA{0x9A, 0xA0, 0xA6, 0xFF}
	}
	short, ok := engineShort[typ]
	if !ok {
		short = "DB"
	}
	circ := canvas.NewCircle(col)
	circ.StrokeWidth = 0
	txt := canvas.NewText(short, color.White)
	txt.TextSize = 11
	txt.TextStyle = fyne.TextStyle{Bold: true}
	txt.Alignment = fyne.TextAlignCenter
	// The Centre container centres the text both axes inside the circle, which
	// a bare canvas.Text (top-left) cannot do.
	badge := container.NewStack(circ, container.NewCenter(txt))
	// The canvas shapes have zero min size, so pin the badge to 26x26.
	return &fixedSize{CanvasObject: badge, w: 26, h: 26}
}

// connDot is the small live indicator: green when the connection is open, grey
// when it is not.
func connDot(live bool) *canvas.Circle {
	dot := canvas.NewCircle(nil)
	if live {
		dot.FillColor = &color.NRGBA{0x2E, 0xCC, 0x71, 0xFF}
	} else {
		dot.FillColor = &color.NRGBA{0xB6, 0xBA, 0xC0, 0xFF}
	}
	return dot
}

// engineType maps the user-facing name to the internal identifier.
func engineType(name string) string {
	switch name {
	case "MySQL":
		return db.DbTypeMySQL
	case "PostgreSQL":
		return db.DbTypePostgres
	case "SQLite":
		return db.DbTypeSQLite
	case "SQL Server":
		return db.DbTypeSQLServer
	case "Oracle":
		return db.DbTypeOracle
	}
	return db.DbTypeMySQL
}

func engineName(typ string) string {
	switch typ {
	case db.DbTypePostgres:
		return "PostgreSQL"
	case db.DbTypeSQLite:
		return "SQLite"
	case db.DbTypeSQLServer:
		return "SQL Server"
	case db.DbTypeOracle:
		return "Oracle"
	}
	return "MySQL"
}

// driverPluginMissing returns the install notice for an engine that needs a
// driver plugin it does not have, or "" when the engine is usable (builtin, or
// its plugin is installed).
func driverPluginMissing(typ string) string {
	if db.BuiltinDrivers()[typ] || db.InstalledDrivers()[typ] {
		return ""
	}
	return engineName(typ) + " 需要安装驱动插件：用 task build-plugins 生成后，把对应可执行文件放到应用旁的 plugins 目录"
}

// buildConnectionsPanel assembles the sidebar's connection section in the
// TablePlus style: a header with a new-connection "+", the connection list
// (one engine-coloured row per saved connection, click to connect), then the
// connect/disconnect toggle and the per-connection edit/delete/test actions.
//
// The original dropdown is kept but hidden: it remains the source of truth for
// selection state (onConnPicked / refreshConnections / updateConnectButton all
// read and write it), so the list just mirrors it instead of forking that logic.
func (c *Client) buildConnectionsPanel() fyne.CanvasObject {
	// -1 (not the zero 0) so a first refresh can tell "nothing chosen yet"
	// from "the user picked row 0" - that is what the ui.json preselect keys
	// on.
	c.picker.idx = -1
	c.picker.sel = widget.NewSelect(nil, func(string) { c.onConnPicked() })
	c.picker.sel.PlaceHolder = "选择连接"
	c.picker.sel.Hide()

	c.picker.toggle = widget.NewButtonWithIcon("连接", theme.ConfirmIcon(), func() {
		c.toggleSelectedConnection()
	})
	// The test button is special: it swaps to "停止" / stop icon while a test
	// is running (see updateTestButton), so it stays a *widget.Button; the
	// other two are icon-only and wrapped with a tooltip for placement.
	testBtn := widget.NewButtonWithIcon("测试", theme.MailComposeIcon(), func() {
		c.testOrStop()
	})
	c.picker.test = testBtn
	editBtn := attachTooltip(iconButton(theme.DocumentCreateIcon(), func() {
		if cfg, ok := c.selectedConn(); ok {
			c.showConnDialog(&cfg)
		}
	}), "编辑连接")
	delBtn := attachTooltip(iconButton(theme.DeleteIcon(), func() {
		cfg, ok := c.selectedConn()
		if !ok {
			return
		}
		dialog.ShowConfirm("确认删除连接",
			fmt.Sprintf("确定删除连接「%s」吗？", cfg.Name),
			func(ok bool) {
				if err := c.store.DeleteConnection(cfg.ID); err == nil {
					c.refreshConnections()
					c.refreshSchema()
				}
			}, c.win)
	}), "删除连接")

	// The list is the visible selector; rebuild it from the saved connections.
	c.picker.list = container.NewVBox()
	c.refreshConnectionList()

	newBtn := attachTooltip(iconButton(theme.ContentAddIcon(), func() {
		c.showConnDialog(nil)
	}), "新建连接")
	header := container.NewBorder(nil, nil, nil, newBtn,
		widget.NewLabelWithStyle("连接", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	listSection := container.NewVBox(header, container.NewScroll(c.picker.list))

	// The connect toggle stays visible so an open connection has an explicit
	// 断开 affordance; the list click only opens one.
	connRow := container.NewBorder(nil, nil, nil, c.picker.toggle, nil)
	// The manage buttons are icons only - the three words cost a full row of
	// width in the narrow sidebar, and a Border pins the group to the right
	// edge like TablePlus's own connection actions. Every icon carries a
	// tooltip (see ui_tooltip.go) so losing the label does not cost
	// discoverability. The test button keeps its label because it doubles as a
	// stop control mid-handshake.
	manageRow := container.NewBorder(nil, nil, nil,
		container.NewHBox(editBtn, delBtn, testBtn), nil)
	return container.NewVBox(listSection, connRow, manageRow)
}

// refreshConnectionList rebuilds the sidebar list from the saved connections,
// reflecting each one's live status with its dot. Called on connect/disconnect
// and whenever the saved set changes.
func (c *Client) refreshConnectionList() {
	if c.picker.list == nil {
		return
	}
	saved := c.store.Saved()
	rows := make([]fyne.CanvasObject, 0, len(saved))
	for i, cfg := range saved {
		rows = append(rows, c.connRowWidget(cfg, i))
	}
	if len(rows) == 0 {
		rows = append(rows, widget.NewLabel("（暂无连接，点右上角 + 新建）"))
	}
	c.picker.list.Objects = rows
	c.picker.list.Refresh()
}

// tapRect is a transparent, clickable rectangle - the row hit area for the
// connection list. It paints no chrome of its own, so the avatar, name and dot
// behind it stay visible, and it tints on hover and when it is the active
// connection.
type tapRect struct {
	widget.BaseWidget
	onTap    func()
	baseFill color.Color
	hovering bool
	rect     *canvas.Rectangle
}

func newTapRect(baseFill color.Color, onTap func()) *tapRect {
	r := &tapRect{onTap: onTap, baseFill: baseFill}
	r.ExtendBaseWidget(r)
	return r
}

func (r *tapRect) CreateRenderer() fyne.WidgetRenderer {
	r.rect = canvas.NewRectangle(r.fillColor())
	return widget.NewSimpleRenderer(r.rect)
}

func (r *tapRect) fillColor() color.Color {
	if r.hovering {
		return &color.NRGBA{0x00, 0x00, 0x00, 0x08}
	}
	if r.baseFill == nil {
		return color.Transparent
	}
	return r.baseFill
}

func (r *tapRect) Refresh() {
	if r.rect != nil {
		r.rect.FillColor = r.fillColor()
		r.rect.Refresh()
	}
}

func (r *tapRect) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap()
	}
}

func (r *tapRect) MouseIn(*desktop.MouseEvent)    { r.hovering = true; r.Refresh() }
func (r *tapRect) MouseMoved(*desktop.MouseEvent) {}
func (r *tapRect) MouseOut()                      { r.hovering = false; r.Refresh() }

// connRowWidget is one line of the connection list: the engine avatar, the
// connection name, and a live dot on the right. A transparent tapRect overlays
// the row to capture the tap while the avatar and label show through; the
// active (current) connection gets a faint tint so it reads as selected.
func (c *Client) connRowWidget(cfg db.ConnConfig, idx int) fyne.CanvasObject {
	avatar := engineAvatar(cfg.Type)
	name := widget.NewLabel(cfg.Name)
	name.Truncation = fyne.TextTruncateEllipsis
	dot := connDot(c.store.ConnectionStatus(cfg.ID))
	body := container.NewHBox(avatar, name, layout.NewSpacer(), dot)
	active := c.picker.idx == idx && c.store.ConnectionStatus(cfg.ID)
	var base color.Color
	if active {
		base = &color.NRGBA{0x1E, 0x6F, 0xD9, 0x1A} // accent at low alpha
	}
	return container.NewMax(newTapRect(base, func() { c.onConnectionRowTap(idx) }), body)
}

// onConnectionRowTap selects a connection from the list and opens it. It mirrors
// what the hidden dropdown would do - set the index, sync the dropdown state,
// then connect if the connection is not already live (an already-open one is
// just re-selected so its schema shows).
func (c *Client) onConnectionRowTap(idx int) {
	saved := c.store.Saved()
	if idx < 0 || idx >= len(saved) {
		return
	}
	cfg := saved[idx]
	c.picker.idx = idx
	if c.picker.sel != nil {
		c.picker.sel.Selected = connOptionLabel(cfg)
		c.picker.sel.Refresh()
	}
	c.onConnPicked()
	if !c.store.ConnectionStatus(cfg.ID) {
		c.onConnect(cfg)
	} else {
		c.refreshSchema()
		c.dbSelRefresh()
	}
	c.refreshConnectionList()
}

// onConnPicked syncs the selection state with the dropdown's chosen name.
func (c *Client) onConnPicked() {
	for i, cfg := range c.store.Saved() {
		if connOptionLabel(cfg) == c.picker.sel.Selected {
			c.picker.idx = i
			c.currentID = cfg.ID
			c.currentDB = cfg.Database
			// Remember the last connection / database so the next start
			// stands on them (ui.json, written on close).
			c.uiSet.LastConnectionID = cfg.ID
			c.uiSet.LastDatabase = cfg.Database
			break
		}
	}
	c.updateConnectButton()
}

// connOptionLabel is how a connection appears in the picker.
func connOptionLabel(cfg db.ConnConfig) string {
	return fmt.Sprintf("%s  (%s)", cfg.Name, engineName(cfg.Type))
}

// selectedConn returns the currently highlighted saved connection.
func (c *Client) selectedConn() (db.ConnConfig, bool) {
	saved := c.store.Saved()
	idx := c.picker.idx
	if idx < 0 || idx >= len(saved) {
		return db.ConnConfig{}, false
	}
	return saved[idx], true
}

// refreshConnections repopulates the picker, keeping the current selection.
// The first refresh (idx still -1) falls back to the connection last used,
// remembered in uiSettings - the 连接 button is still the one that opens it.
func (c *Client) refreshConnections() {
	if c.picker.sel == nil {
		return
	}
	saved := c.store.Saved()
	if c.picker.idx < 0 && c.uiSet.LastConnectionID != "" {
		for i, cfg := range saved {
			if cfg.ID == c.uiSet.LastConnectionID {
				c.picker.idx = i
				break
			}
		}
	}
	if c.picker.idx < 0 && len(saved) > 0 {
		// No memory yet (first run): keep the long-standing behaviour of
		// standing on the first saved connection.
		c.picker.idx = 0
	}
	names := make([]string, len(saved))
	selected := ""
	for i, cfg := range saved {
		names[i] = connOptionLabel(cfg)
		if i == c.picker.idx {
			selected = names[i]
		}
	}
	c.picker.sel.Options = names
	c.picker.sel.Selected = selected
	c.picker.sel.Refresh()
	c.updateConnectButton()
	c.refreshConnectionList()
}

// toggleSelectedConnection connects or disconnects the highlighted
// connection. While a handshake is in flight the same button stops it.
func (c *Client) toggleSelectedConnection() {
	if c.connectCancel != nil {
		c.stopConnect()
		return
	}
	cfg, ok := c.selectedConn()
	if !ok {
		return
	}
	if c.store.ConnectionStatus(cfg.ID) {
		c.onDisconnect(cfg.ID)
		return
	}
	c.onConnect(cfg)
}

// updateConnectButton reflects whether the selected connection is live - or
// a handshake is in flight, when the button offers 停止 instead.
func (c *Client) updateConnectButton() {
	if c.picker.toggle == nil {
		return
	}
	if c.connectCancel != nil {
		c.picker.toggle.SetText("停止")
		c.picker.toggle.Icon = theme.MediaStopIcon()
		c.picker.toggle.Refresh()
		return
	}
	cfg, ok := c.selectedConn()
	if ok && c.store.ConnectionStatus(cfg.ID) {
		c.picker.toggle.SetText("断开")
		c.picker.toggle.Icon = theme.CancelIcon()
	} else {
		c.picker.toggle.SetText("连接")
		c.picker.toggle.Icon = theme.ConfirmIcon()
	}
	c.picker.toggle.Refresh()
}

// testOrStop drives the panel's 测试 button: it starts a test of the
// highlighted connection, or stops one that is already in flight - which is
// the point, a dead server used to hold the button hostage.
func (c *Client) testOrStop() {
	if c.testCancel != nil {
		c.stopTest()
		return
	}
	if cfg, ok := c.selectedConn(); ok {
		c.testAndReport(cfg)
	}
}

// stopTest abandons the in-flight handshake. The abandoned round's callback
// is dropped by background, so the button state is reset right here.
func (c *Client) stopTest() {
	if c.testCancel == nil {
		return
	}
	c.testCancel()
	c.testCancel = nil
	c.setStatus("测试已取消")
	c.updateTestButton()
}

// testAndReport tests a connection and reports the outcome. The ping runs on
// the cancellable background primitive so the window stays live while it
// waits on the server, and 停止 hands that wait back to the user.
func (c *Client) testAndReport(cfg db.ConnConfig) {
	if c.testCancel != nil {
		return // one handshake at a time; the button is showing 停止 already
	}
	c.setStatus("正在测试 " + cfg.Name + " ...")
	var testErr error
	c.testCancel = c.background(
		func(ctx context.Context) {
			connDB, err := db.TestConnection(ctx, cfg)
			if err == nil {
				connDB.Close()
			}
			testErr = err
		},
		func() {
			c.testCancel = nil
			c.updateTestButton()
			if testErr != nil {
				c.setStatusError("测试失败：" + testErr.Error())
				dialog.ShowError(testErr, c.win)
				return
			}
			c.setStatus("测试成功：" + cfg.Name)
			dialog.ShowInformation("测试连接", "连接成功：\n"+cfg.Name, c.win)
		},
	)
	c.updateTestButton()
}

// updateTestButton mirrors whether a handshake is running: 测试 → 停止.
func (c *Client) updateTestButton() {
	if c.picker.test == nil {
		return
	}
	if c.testCancel != nil {
		c.picker.test.SetText("停止")
		c.picker.test.Icon = theme.MediaStopIcon()
	} else {
		c.picker.test.SetText("测试")
		c.picker.test.Icon = theme.MailComposeIcon()
	}
	c.picker.test.Refresh()
}

// onConnect opens the connection and refreshes the schema. The handshake
// runs on the cancellable background primitive; a dead server therefore
// ends in 取消, not in a button that will never stop saying 正在连接.
// Everything that touches a widget runs back on the main goroutine.
func (c *Client) onConnect(cfg db.ConnConfig) {
	if c.connectCancel != nil {
		return // already in flight; the button is showing 停止
	}
	c.setStatus("正在连接到 " + cfg.Name + " ...")
	var connErr error
	c.connectCancel = c.background(
		func(ctx context.Context) {
			// Only the store is touched here: the session fields belong to the
			// UI thread, and its dispatched callback below sets them.
			connErr = c.store.ConnectContext(ctx, cfg)
		},
		func() {
			c.connectCancel = nil
			c.updateConnectButton()
			if connErr != nil {
				c.setStatusError("连接失败：" + connErr.Error())
				dialog.ShowError(connErr, c.win)
				return
			}
			c.currentID = cfg.ID
			c.currentDB = cfg.Database
			c.setStatus("已连接：" + cfg.Name)
			c.refreshSchema()
			c.dbSelRefresh()
			c.updateConnectButton()
			c.refreshConnectionList()
			c.focusEditor() // so the next keystrokes land in the editor, not the button
		})
	c.updateConnectButton()
}

// stopConnect abandons the in-flight handshake; the abandoned round's
// callback is dropped by background, so the button is reset here.
func (c *Client) stopConnect() {
	if c.connectCancel == nil {
		return
	}
	c.connectCancel()
	c.connectCancel = nil
	c.setStatus("连接已取消")
	c.updateConnectButton()
}

func (c *Client) onDisconnect(id string) {
	name := id
	if cfg, ok := c.store.FindConnection(id); ok {
		name = cfg.Name
	}
	c.disconnect(id)
	c.setStatus("已断开：" + name)
	c.refreshSchema()
	c.updateConnectButton()
	c.refreshConnectionList()
}
