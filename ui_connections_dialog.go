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
// The controls and their interplay live in connFields; this function only wires
// them to the store and to the dialog frame.
func (c *Client) showConnDialog(edit *db.ConnConfig) {
	f := c.newConnFields(edit)

	var d *dialog.CustomDialog

	save := func() {
		cfg := f.config()
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
		c.testAndReport(f.config())
	}

	saveBtn := widget.NewButtonWithIcon("保存", theme.ConfirmIcon(), save)
	testBtn := widget.NewButtonWithIcon("测试", theme.MailComposeIcon(), test)
	buttons := container.NewHBox(testBtn, layout.NewSpacer(), saveBtn)

	// On a platform that cannot seal a password, the form says so rather than
	// letting the user assume 保存 encrypts it.
	var bottom fyne.CanvasObject = buttons
	if note := secretStorageNote(db.SecretsSealed()); note != "" {
		label := widget.NewLabel(note)
		label.Wrapping = fyne.TextWrapWord
		label.Importance = widget.HighImportance
		bottom = container.NewVBox(label, buttons)
	}

	title := "新建连接"
	if edit != nil {
		title = "编辑连接"
	}
	content := container.NewBorder(nil, bottom, nil, nil, f.form())
	d = dialog.NewCustom(title, "取消", content, c.win)
	d.Resize(fyne.NewSize(460, 440))
	d.Show()
}

// connFields bundles the create/edit form's controls. It is passed around by
// pointer because the engine-switch and 加密 closures mutate sslStored, so the
// dialog and the widgets must share one instance.
type connFields struct {
	nameE, hostE, portE, userE, passE, dbE *widget.Entry
	typeSel, sslSel                        *widget.Select
	// filePicker opens an existing database file; it only shows for SQLite.
	filePicker *widget.Button
	// sslStored is the mode string the 加密 dropdown currently stands for.
	sslStored string
}

// newConnFields builds the controls, seeded from edit when editing an existing
// connection and from friendly defaults when creating one.
func (c *Client) newConnFields(edit *db.ConnConfig) *connFields {
	f := &connFields{
		nameE:   widget.NewEntry(),
		hostE:   widget.NewEntry(),
		portE:   widget.NewEntry(),
		userE:   widget.NewEntry(),
		passE:   widget.NewPasswordEntry(),
		dbE:     widget.NewEntry(),
		typeSel: widget.NewSelect([]string{"MySQL", "PostgreSQL", "SQLite", "SQL Server", "Oracle"}, nil),
		// The 加密 list is engine-specific: each driver parses a different
		// vocabulary, and offering a mode the driver ignores is how a connection
		// ends up unencrypted while the form says otherwise.
		sslSel: widget.NewSelect(nil, nil),
	}
	f.filePicker = widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		c.pickDatabaseFile(f.dbE)
	})
	f.filePicker.Importance = widget.LowImportance

	f.sslSel.OnChanged = func(label string) {
		f.sslStored = sslValueFor(sslChoicesFor(engineType(f.typeSel.Selected)), label)
	}

	if edit != nil {
		f.nameE.SetText(edit.Name)
		f.typeSel.SetSelected(engineName(edit.Type))
		f.hostE.SetText(edit.Host)
		if edit.Port > 0 {
			f.portE.SetText(strconv.Itoa(edit.Port))
		}
		f.userE.SetText(edit.Username)
		f.passE.SetText(edit.Password)
		f.dbE.SetText(edit.Database)
		f.applyEngineSSL(edit.SSLMode)
	} else {
		f.typeSel.SetSelected("MySQL")
		f.portE.SetText(defaultPortFor(db.DbTypeMySQL))
		f.applyEngineSSL("")
	}

	// Friendly default ports per engine when creating a new connection.
	f.typeSel.OnChanged = func(t string) {
		f.updatePicker()
		// Each engine asks for its own encryption vocabulary.
		f.applyEngineSSL(f.sslStored)
		if edit != nil {
			return
		}
		f.portE.SetText(defaultPortFor(engineType(t)))
	}
	f.updatePicker()
	return f
}

// form is the create/edit form body.
func (f *connFields) form() *widget.Form {
	dbRow := container.NewBorder(nil, nil, nil, f.filePicker, f.dbE)
	return widget.NewForm(
		widget.NewFormItem("名称", f.nameE),
		widget.NewFormItem("类型", f.typeSel),
		widget.NewFormItem("主机", f.hostE),
		widget.NewFormItem("端口", f.portE),
		widget.NewFormItem("用户名", f.userE),
		widget.NewFormItem("密码", f.passE),
		widget.NewFormItem("数据库", dbRow),
		widget.NewFormItem("加密", f.sslSel),
	)
}

// applyEngineSSL repopulates the 加密 dropdown for the selected engine and
// carries the current choice over, so switching the type cannot rewrite a
// stored mode behind the user's back.
func (f *connFields) applyEngineSSL(carry string) {
	choices := sslChoicesFor(engineType(f.typeSel.Selected))
	f.sslSel.SetOptions(sslOptionLabels(choices, carry))
	if label := sslChoiceLabel(choices, carry); label != "" {
		f.sslSel.SetSelected(label)
		return
	}
	f.sslStored = carry
}

// updatePicker shows the file picker only while the engine reads a file.
func (f *connFields) updatePicker() {
	if engineType(f.typeSel.Selected) == db.DbTypeSQLite {
		f.filePicker.Show()
		return
	}
	f.filePicker.Hide()
}

// config reads the form controls into a db.ConnConfig.
func (f *connFields) config() db.ConnConfig {
	port, _ := strconv.Atoi(f.portE.Text)
	engine := engineType(f.typeSel.Selected)
	cfg := db.ConnConfig{
		Name:     f.nameE.Text,
		Type:     engine,
		Host:     f.hostE.Text,
		Port:     port,
		Username: f.userE.Text,
		Password: f.passE.Text,
		Database: f.dbE.Text,
		// The dropdown shows labels; the connection stores the mode string
		// each engine's driver expects.
		SSLMode: sslValueFor(sslChoicesFor(engine), f.sslSel.Selected),
	}
	if cfg.Name == "" {
		cfg.Name = fmt.Sprintf("%s@%s", cfg.Username, cfg.Host)
	}
	return cfg
}

// defaultPortFor answers the port an engine listens on by default. SQLite has
// no port, so it gets "" and the field clears.
func defaultPortFor(engine string) string {
	switch engine {
	case db.DbTypeMySQL:
		return "3306"
	case db.DbTypePostgres:
		return "5432"
	case db.DbTypeSQLServer:
		return "1433"
	case db.DbTypeOracle:
		return "1521"
	}
	return ""
}

// pickDatabaseFile opens a file chooser and writes the chosen path into entry.
func (c *Client) pickDatabaseFile(entry *widget.Entry) {
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
		entry.SetText(p)
	}, c.win)
	fd.Resize(fyne.NewSize(600, 480))
	fd.Show()
}

// secretStorageNote words what SecretsSealed answers: sealed platforms store the
// password behind a per-user key and need no note, everywhere else the saved
// file holds the password in clear text and only its permissions protect it.
func secretStorageNote(sealed bool) string {
	if sealed {
		return ""
	}
	return "注意：本机没有可用的系统密钥库，保存后密码以明文写在连接文件中（仅靠文件权限 0600 保护）。"
}

// sslChoice is one entry of the 加密 dropdown: the label the user reads and the
// mode string stored in the connection. The lists are per engine because each
// driver parses a different vocabulary - see db.mssqlEncrypt for the SQL Server
// side, which cannot take "strict" at all.
type sslChoice struct {
	label, value string
}

var sslChoicesByEngine = map[string][]sslChoice{
	db.DbTypeSQLServer: {
		// The secure default comes first: an unset mode encrypts and verifies.
		{"加密并校验证书（默认）", "true"},
		{"加密，信任服务器证书（自签）", "trust"},
		{"不加密", "disable"},
	},
	db.DbTypeMySQL: {
		// The MySQL driver here only turns TLS on for the value "true".
		{"不加密（默认）", ""},
		{"加密", "true"},
	},
	db.DbTypePostgres: {
		{"默认（按服务端）", ""},
		{"不加密", "disable"},
		{"尽力加密", "prefer"},
		{"加密", "require"},
		{"加密并校验 CA", "verify-ca"},
		{"加密并校验 CA 与主机名", "verify-full"},
	},
}

// sslChoicesFor answers the engine's list; engines with no TLS knob of their
// own (SQLite, Oracle) keep the PostgreSQL vocabulary the dialog already
// stored for them, so editing such a connection cannot lose its mode.
func sslChoicesFor(dbType string) []sslChoice {
	if choices, ok := sslChoicesByEngine[dbType]; ok {
		return choices
	}
	return sslChoicesByEngine[db.DbTypePostgres]
}

// sslOptionLabels lists the dropdown's entries, appending a stored mode the
// current engine's list does not name rather than dropping it.
func sslOptionLabels(choices []sslChoice, stored string) []string {
	labels := make([]string, 0, len(choices)+1)
	known := false
	for _, ch := range choices {
		labels = append(labels, ch.label)
		if ch.value == stored {
			known = true
		}
	}
	if !known && stored != "" {
		labels = append(labels, stored)
	}
	return labels
}

// sslChoiceLabel finds the label for a stored mode, or "" when the mode is not
// one of this engine's options and has no label to show yet.
func sslChoiceLabel(choices []sslChoice, stored string) string {
	for _, ch := range choices {
		if ch.value == stored {
			return ch.label
		}
	}
	if stored == "" {
		return ""
	}
	return stored // carried through as its own label by sslOptionLabels
}

// sslValueFor maps the chosen label back to the stored mode; an unknown label
// is the carried-through mode itself.
func sslValueFor(choices []sslChoice, label string) string {
	for _, ch := range choices {
		if ch.label == label {
			return ch.value
		}
	}
	return label
}
