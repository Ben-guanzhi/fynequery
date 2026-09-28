package main

import (
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
	c.picker.sel = widget.NewSelect(nil, func(string) { c.onConnPicked() })
	c.picker.sel.PlaceHolder = "选择连接"

	c.picker.toggle = widget.NewButtonWithIcon("连接", theme.ConfirmIcon(), func() {
		c.toggleSelectedConnection()
	})
	testBtn := widget.NewButtonWithIcon("测试", theme.MailComposeIcon(), func() {
		if cfg, ok := c.selectedConn(); ok {
			c.testAndReport(cfg)
		}
	})
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
		dialog.ShowConfirm("删除连接",
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
func (c *Client) refreshConnections() {
	if c.picker.sel == nil {
		return
	}
	saved := c.store.Saved()
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

// toggleSelectedConnection connects or disconnects the highlighted connection.
func (c *Client) toggleSelectedConnection() {
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

// updateConnectButton reflects whether the selected connection is live.
func (c *Client) updateConnectButton() {
	if c.picker.toggle == nil {
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

// testAndReport tests a saved connection and reports the outcome. The ping
// runs off the UI thread so the window stays live while it waits on the server.
func (c *Client) testAndReport(cfg db.ConnConfig) {
	c.setStatus("正在测试 " + cfg.Name + " ...")
	var testErr error
	c.runAsync(
		func() {
			connDB, err := db.TestConnection(cfg)
			if err == nil {
				connDB.Close()
			}
			testErr = err
		},
		func() {
			if testErr != nil {
				c.setStatus("测试失败：" + testErr.Error())
				dialog.ShowError(testErr, c.win)
				return
			}
			c.setStatus("测试成功：" + cfg.Name)
			dialog.ShowInformation("测试连接", "连接成功：\n"+cfg.Name, c.win)
		},
	)
}

// onConnect opens the connection and refreshes the schema. The handshake
// (and, on failure, the timeout) runs off the UI thread; everything that
// touches a widget runs back on the main goroutine.
func (c *Client) onConnect(cfg db.ConnConfig) {
	c.setStatus("正在连接到 " + cfg.Name + " ...")
	var connErr error
	c.runAsync(
		func() {
			// Only the store is touched here: the session fields belong to the
			// UI thread, and its dispatched callback below sets them.
			connErr = c.store.Connect(cfg)
		},
		func() {
			if connErr != nil {
				c.setStatus("连接失败：" + connErr.Error())
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
		},
	)
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
