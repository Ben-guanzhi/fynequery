package main

import (
	"fmt"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// showConnDialog displays the create/edit form. edit == nil means "create new".
func (c *Client) showConnDialog(edit *db.ConnConfig) {
	nameE := widget.NewEntry()
	typeSel := widget.NewSelect([]string{"MySQL", "PostgreSQL", "SQLite", "SQL Server", "Oracle"}, nil)
	hostE := widget.NewEntry()
	portE := widget.NewEntry()
	userE := widget.NewEntry()
	passE := widget.NewPasswordEntry()
	dbE := widget.NewEntry()
	pickBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			defer r.Close()
			p := r.URI().Path()
			// Windows file URIs carry a leading slash before the drive letter.
			if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
				p = p[1:]
			}
			dbE.SetText(p)
		}, c.win)
		fd.Resize(fyne.NewSize(600, 480))
		fd.Show()
	})
	pickBtn.Importance = widget.LowImportance
	dbRow := container.NewBorder(nil, nil, nil, pickBtn, dbE)
	sslSel := widget.NewSelect([]string{"", "false", "true", "disable", "require", "verify-ca", "verify-full"}, nil)

	if edit != nil {
		nameE.SetText(edit.Name)
		typeSel.SetSelected(engineName(edit.Type))
		hostE.SetText(edit.Host)
		if edit.Port > 0 {
			portE.SetText(strconv.Itoa(edit.Port))
		}
		userE.SetText(edit.Username)
		passE.SetText(edit.Password)
		dbE.SetText(edit.Database)
		sslSel.SetSelected(edit.SSLMode)
	} else {
		typeSel.SetSelected("MySQL")
		portE.SetText("3306")
	}

	// Friendly default ports per engine when creating a new connection.
	typeSel.OnChanged = func(t string) {
		if pickBtn != nil {
			if engineType(t) == db.DbTypeSQLite {
				pickBtn.Show()
			} else {
				pickBtn.Hide()
			}
		}
		if edit != nil {
			return
		}
		switch engineType(t) {
		case db.DbTypeMySQL:
			portE.SetText("3306")
		case db.DbTypePostgres:
			portE.SetText("5432")
		case db.DbTypeSQLite:
			portE.SetText("")
		case db.DbTypeSQLServer:
			portE.SetText("1433")
		case db.DbTypeOracle:
			portE.SetText("1521")
		}
	}
	if typeSel.Selected == "SQLite" {
		pickBtn.Show()
	} else {
		pickBtn.Hide()
	}

	form := widget.NewForm(
		widget.NewFormItem("名称", nameE),
		widget.NewFormItem("类型", typeSel),
		widget.NewFormItem("主机", hostE),
		widget.NewFormItem("端口", portE),
		widget.NewFormItem("用户名", userE),
		widget.NewFormItem("密码", passE),
		widget.NewFormItem("数据库", dbRow),
		widget.NewFormItem("SSL", sslSel),
	)

	var d *dialog.CustomDialog

	save := func() {
		cfg := c.configFromFields(nameE, typeSel, hostE, portE, userE, passE, dbE, sslSel)
		cfg.Options = map[string]string{}
		if edit != nil {
			cfg.ID = edit.ID
			if err := c.store.UpdateConnection(cfg); err != nil {
				dialog.ShowError(err, c.win)
				return
			}
		} else {
			c.store.AddConnection(cfg)
		}
		c.refreshConnections()
		if d != nil {
			d.Hide()
		}
	}

	test := func() {
		cfg := c.configFromFields(nameE, typeSel, hostE, portE, userE, passE, dbE, sslSel)
		c.testAndReport(cfg)
	}

	saveBtn := widget.NewButtonWithIcon("保存", theme.ConfirmIcon(), save)
	testBtn := widget.NewButtonWithIcon("测试", theme.MailComposeIcon(), test)
	buttons := container.NewHBox(testBtn, layout.NewSpacer(), saveBtn)

	title := "新建连接"
	if edit != nil {
		title = "编辑连接"
	}
	content := container.NewBorder(nil, buttons, nil, nil, form)
	d = dialog.NewCustom(title, "取消", content, c.win)
	d.Resize(fyne.NewSize(460, 440))
	d.Show()
}

// configFromFields reads the form controls into a db.ConnConfig.
func (c *Client) configFromFields(nameE *widget.Entry, typeSel *widget.Select, hostE *widget.Entry,
	portE *widget.Entry, userE *widget.Entry, passE *widget.Entry, dbE *widget.Entry,
	sslSel *widget.Select) db.ConnConfig {

	port, _ := strconv.Atoi(portE.Text)
	cfg := db.ConnConfig{
		Name:     nameE.Text,
		Type:     engineType(typeSel.Selected),
		Host:     hostE.Text,
		Port:     port,
		Username: userE.Text,
		Password: passE.Text,
		Database: dbE.Text,
		SSLMode:  sslSel.Selected,
	}
	if cfg.Name == "" {
		cfg.Name = fmt.Sprintf("%s@%s", cfg.Username, cfg.Host)
	}
	return cfg
}
