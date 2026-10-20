package main

import (
	"context"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

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

// buildConnectionsPanel assembles the sidebar's connection section: the
// picker with the connect toggle beside it, then the manage actions.
func (c *Client) buildConnectionsPanel() fyne.CanvasObject {
	// -1 (not the zero 0) so a first refresh can tell "nothing chosen yet"
	// from "the user picked row 0" - that is what the ui.json preselect keys
	// on.
	c.picker.idx = -1
	c.picker.sel = widget.NewSelect(nil, func(string) { c.onConnPicked() })
	c.picker.sel.PlaceHolder = "选择连接"

	c.picker.toggle = widget.NewButtonWithIcon("连接", theme.ConfirmIcon(), func() {
		c.toggleSelectedConnection()
	})
	testBtn := widget.NewButtonWithIcon("测试", theme.MailComposeIcon(), func() {
		c.testOrStop()
	})
	c.picker.test = testBtn
	editBtn := widget.NewButtonWithIcon("编辑", theme.DocumentCreateIcon(), func() {
		if cfg, ok := c.selectedConn(); ok {
			c.showConnDialog(&cfg)
		}
	})
	delBtn := widget.NewButtonWithIcon("删除", theme.DeleteIcon(), func() {
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
	})

	connRow := container.NewBorder(nil, nil, nil, c.picker.toggle, c.picker.sel)
	manageRow := container.NewGridWithColumns(3, testBtn, editBtn, delBtn)
	return container.NewVBox(connRow, manageRow)
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
}
