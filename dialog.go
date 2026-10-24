package main

// Connection sheet styled after TablePlus's connection form: a DB-type
// selector + host/port/user/password/database fields, plus status colour, safe
// mode and keyring options. The same form backs two flows:
//   - "New connection": submit dials, saves and opens the workspace.
//   - "Edit connection": submit only saves (no dial) and refreshes the list.

import (
	"fmt"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

func showNewConnectionDialog(w fyne.Window, onConnect func(Connection, db.Session)) {
	showConnectionDialog(w, Connection{}, onConnect)
}

// showConnectionDialog opens the "New Connection" sheet. seed pre-fills the
// form (Import from URL); an empty seed starts on conventional defaults.
// Submitting dials the database, saves the connection and opens the workspace.
func showConnectionDialog(w fyne.Window, seed Connection, onConnect func(Connection, db.Session)) {
	cf := newConnectionForm(w, seed)
	if cf == nil {
		return
	}
	test := newTestConnectionButton(w, cf.build)

	d := dialog.NewCustom("New Connection", "", connectionSheetBody(cf, test), w)
	dialAndSave := func(c Connection) {
		progress := dialog.NewProgressInfinite("Connecting", fmt.Sprintf("Connecting to %s (%s)...", c.Name, c.Host), w)
		progress.Show()
		safeGo("dialog", func() {
			sess, err := dialConn(c)
			safeDo(func() {
				progress.Hide()
				if err != nil {
					dialog.NewError(err, w).Show()
					return
				}
				if err := upsertConnection(c); err != nil {
					dialog.NewError(fmt.Errorf("save connection: %w", err), w).Show()
					return // keep the sheet open; the connection was not saved
				}
				d.Hide() // dismiss the form before opening the workspace
				if onConnect != nil {
					onConnect(c, sess)
				}
			})
		})
	}
	cf.form.OnSubmit = func() {
		c := cf.build()
		// Port must be numeric when set (SQLite is file-based and skips it).
		if c.Driver != "sqlite" && c.Port != "" {
			if _, err := strconv.Atoi(c.Port); err != nil {
				showInfo(w, "New Connection", "端口必须是数字。")
				return
			}
		}
		// Warn before silently overwriting an existing same-named connection
		// (the auto-generated "MySQL localhost" name collides easily).
		if existing, lerr := loadConnections(); lerr == nil {
			for _, e := range existing {
				if e.Name == c.Name {
					dialog.NewConfirm("覆盖连接",
						fmt.Sprintf("已存在同名连接 %q，覆盖？", c.Name),
						func(ok bool) {
							if ok {
								dialAndSave(c)
							}
						}, w).Show()
					return
				}
			}
		}
		dialAndSave(c)
	}
	cf.form.SubmitText = "Connect"
	cf.form.CancelText = "Cancel"
	cf.form.OnCancel = func() { d.Hide() }

	d.Resize(fyne.NewSize(440, 620))
	d.Show()
}

// showEditConnectionDialog opens the "Edit Connection" sheet pre-filled from
// orig. Submitting only saves (it never dials); onSaved refreshes the caller's
// list afterwards. Grouping/tags are preserved (the sheet does not edit them).
func showEditConnectionDialog(w fyne.Window, orig Connection, onSaved func()) {
	seed := orig
	seed.resolvePassword() // prefill the (possibly keyring-stored) password
	cf := newConnectionForm(w, seed)
	if cf == nil {
		return
	}
	test := newTestConnectionButton(w, cf.build)

	d := dialog.NewCustom("Edit Connection", "", connectionSheetBody(cf, test), w)
	cf.form.OnSubmit = func() {
		if err := applyConnectionEdit(orig, cf.build()); err != nil {
			dialog.NewError(err, w).Show()
			return
		}
		d.Hide()
		if onSaved != nil {
			onSaved()
		}
	}
	cf.form.SubmitText = "Save"
	cf.form.CancelText = "Cancel"
	cf.form.OnCancel = func() { d.Hide() }

	d.Resize(fyne.NewSize(440, 620))
	d.Show()
}

// applyConnectionEdit persists an edited connection: it keeps the fields the
// sheet does not expose (Group/Tags), rejects an empty name, and — when the
// name changed — removes the old entry (and its keyring secret) before saving
// the new one. Pure with respect to the UI so it can be unit-tested.
func applyConnectionEdit(orig, edited Connection) error {
	edited.Group = orig.Group
	edited.Tags = orig.Tags
	if edited.Name == "" {
		return fmt.Errorf("connection name cannot be empty")
	}
	if edited.Name != orig.Name {
		if err := deleteConnection(orig.Name); err != nil {
			return fmt.Errorf("rename: %w", err)
		}
	}
	return upsertConnection(edited)
}

// connectionForm bundles the widgets of the connection sheet so the new/edit
// flows share one form definition.
type connectionForm struct {
	picker *widget.Select // top type selector (also the form's "Type" item)
	form   *widget.Form
	build  func() Connection
}

// newConnectionForm builds the sheet pre-filled from seed (empty seed =
// conventional defaults). Returns nil (after surfacing an error) when no
// drivers are registered.
func newConnectionForm(w fyne.Window, seed Connection) *connectionForm {
	infos := db.Drivers()
	if len(infos) == 0 {
		dialog.NewError(fmt.Errorf("no database drivers registered"), w).Show()
		return nil
	}
	typeNames := make([]string, len(infos))
	codeByName := map[string]string{}
	nameByCode := map[string]string{}
	for i, in := range infos {
		typeNames[i] = in.Name
		codeByName[in.Name] = in.Code
		nameByCode[in.Code] = in.Name
	}

	picker := widget.NewSelect(typeNames, nil)
	if _, ok := codeByName["MySQL"]; ok {
		picker.SetSelected("MySQL")
	} else {
		picker.SetSelected(typeNames[0])
	}

	host := widget.NewEntry()
	host.SetPlaceHolder("127.0.0.1")
	host.SetText("localhost")
	port := widget.NewEntry()
	port.SetPlaceHolder("3306")
	user := widget.NewEntry()
	user.SetPlaceHolder("root")
	pass := widget.NewPasswordEntry()
	dbname := widget.NewEntry()
	dbname.SetPlaceHolder("database")

	// Fill conventional defaults whenever the type changes.
	setDefaults := func(name string) {
		code := codeByName[name]
		port.SetText(db.DefaultPort(code))
		switch code {
		case "postgres":
			user.SetText("postgres")
			dbname.SetText("postgres")
		case "mssql":
			user.SetText("sa")
			dbname.SetText("master")
		case "oracle":
			user.SetText("system")
			dbname.SetText("ORCLPDB1")
		case "sqlite":
			// File-based: only the Database box (file path) matters.
			host.SetText("")
			user.SetText("")
			pass.SetText("")
			dbname.SetText("")
			dbname.SetPlaceHolder("C:\\path\\to\\file.db")
			host.SetPlaceHolder("(unused for SQLite)")
		case "redis":
			// Redis "database" is the logical DB number; 0 is the default.
			user.SetText("")
			dbname.SetText("0")
			dbname.SetPlaceHolder("0 (DB index)")
			host.SetPlaceHolder("localhost")
		default: // mysql
			user.SetText("root")
			dbname.SetText("")
		}
	}
	setDefaults(picker.Selected)
	picker.OnChanged = setDefaults

	// Pre-fill from the seed (edit / import), overriding the defaults above.
	if seed.Driver != "" {
		if n, ok := nameByCode[seed.Driver]; ok {
			picker.SetSelected(n) // triggers setDefaults, then we overwrite below
		}
	}
	if seed.Host != "" {
		host.SetText(seed.Host)
	}
	if seed.Port != "" {
		port.SetText(seed.Port)
	}
	if seed.User != "" {
		user.SetText(seed.User)
	}
	if seed.Password != "" {
		pass.SetText(seed.Password)
	}
	if seed.Database != "" {
		dbname.SetText(seed.Database)
	}

	name := widget.NewEntry()
	name.SetPlaceHolder("My connection")
	if seed.Name != "" {
		name.SetText(seed.Name)
	}

	colorSel := widget.NewSelect(statusColorNames, nil)
	if seed.StatusColor != "" {
		colorSel.SetSelected(seed.StatusColor)
	} else {
		colorSel.SetSelected("None")
	}

	safeSel := widget.NewSelect(safeModeNames, nil)
	if lv := safeModeName(seed.SafeMode); lv != "" {
		safeSel.SetSelected(lv)
	}

	keyringChk := widget.NewCheck("Store password in Windows keyring", func(bool) {})
	if keyringAvailable() {
		keyringChk.SetChecked(seed.UseKeyring)
	} else {
		keyringChk.Disable() // non-Windows platforms keep the plaintext fallback
	}

	// --- SSH tunnel section ---
	sshEnable := widget.NewCheck("Use SSH tunnel", nil)
	sshHost := widget.NewEntry()
	sshHost.SetPlaceHolder("bastion.example.com")
	sshPort := widget.NewEntry()
	sshPort.SetText("22")
	sshUser := widget.NewEntry()
	sshUser.SetPlaceHolder("ssh user")
	sshAuthMode := widget.NewSelect([]string{"key", "password"}, nil)
	sshAuthMode.SetSelected("key")
	sshPass := widget.NewPasswordEntry()
	sshPass.SetPlaceHolder("password")
	sshKeyPath := widget.NewEntry()
	sshKeyPath.SetPlaceHolder("C:\\keys\\id_rsa")
	sshKeyPass := widget.NewPasswordEntry()
	sshKeyPass.SetPlaceHolder("(optional)")

	sshFields := []*widget.Entry{sshHost, sshPort, sshUser, sshPass, sshKeyPath, sshKeyPass}
	setSSHEnabled := func(on bool) {
		for _, e := range sshFields {
			if on {
				e.Enable()
			} else {
				e.Disable()
			}
		}
		if on {
			sshAuthMode.Enable()
		} else {
			sshAuthMode.Disable()
		}
	}
	sshEnable.OnChanged = func(on bool) { setSSHEnabled(on) }
	setSSHEnabled(false) // start collapsed (disabled)

	// Pre-fill SSH from the seed (edit / import); enabled fields only when set.
	if seed.SSH.Enabled {
		sshEnable.SetChecked(true)
		if seed.SSH.Host != "" {
			sshHost.SetText(seed.SSH.Host)
		}
		if seed.SSH.Port != "" {
			sshPort.SetText(seed.SSH.Port)
		}
		if seed.SSH.User != "" {
			sshUser.SetText(seed.SSH.User)
		}
		if seed.SSH.AuthMode != "" {
			sshAuthMode.SetSelected(seed.SSH.AuthMode)
		}
		if seed.SSH.Password != "" {
			sshPass.SetText(seed.SSH.Password)
		}
		if seed.SSH.KeyPath != "" {
			sshKeyPath.SetText(seed.SSH.KeyPath)
		}
		if seed.SSH.KeyPassphrase != "" {
			sshKeyPass.SetText(seed.SSH.KeyPassphrase)
		}
		setSSHEnabled(true)
	}

	form := widget.NewForm(
		widget.NewFormItem("Name", name),
		widget.NewFormItem("Host", host),
		widget.NewFormItem("Port", port),
		widget.NewFormItem("User", user),
		widget.NewFormItem("Password", pass),
		widget.NewFormItem("Database", dbname),
		widget.NewFormItem("Status Color", colorSel),
		widget.NewFormItem("Safe Mode", safeSel),
		widget.NewFormItem("Password Storage", keyringChk),
		widget.NewFormItem("Use SSH Tunnel", sshEnable),
		widget.NewFormItem("SSH Host", sshHost),
		widget.NewFormItem("SSH Port", sshPort),
		widget.NewFormItem("SSH User", sshUser),
		widget.NewFormItem("SSH Auth", sshAuthMode),
		widget.NewFormItem("SSH Password", sshPass),
		widget.NewFormItem("SSH Key Path", sshKeyPath),
		widget.NewFormItem("SSH Key Pass", sshKeyPass),
	)

	buildConn := func() Connection {
		driverCode := codeByName[picker.Selected]
		cname := name.Text
		if cname == "" {
			if driverCode == "sqlite" {
				cname = "SQLite " + dbname.Text
			} else {
				cname = picker.Selected + " " + host.Text
			}
		}
		return Connection{
			Name:        cname,
			Driver:      driverCode,
			Host:        host.Text,
			Port:        port.Text,
			User:        user.Text,
			Password:    pass.Text,
			Database:    dbname.Text,
			Group:       "Work",
			Tags:        []string{"loc"},
			StatusColor: colorSel.Selected,
			SafeMode:    safeModeForName(safeSel.Selected),
			UseKeyring:  keyringChk.Checked && keyringAvailable() && pass.Text != "",
			SSH: db.SSHConfig{
				Enabled:       sshEnable.Checked,
				Host:          sshHost.Text,
				Port:          sshPort.Text,
				User:          sshUser.Text,
				AuthMode:      sshAuthMode.Selected,
				Password:      sshPass.Text,
				KeyPath:       sshKeyPath.Text,
				KeyPassphrase: sshKeyPass.Text,
			},
		}
	}

	return &connectionForm{picker: picker, form: form, build: buildConn}
}

// connectionSheetBody lays out the shared sheet: prominent type selector, the
// form, and an extra action row (the Test button).
func connectionSheetBody(cf *connectionForm, extra fyne.CanvasObject) fyne.CanvasObject {
	return container.NewPadded(container.NewVBox(
		container.NewHBox(layout.NewSpacer(), cf.picker, layout.NewSpacer()),
		spacerH(6),
		container.NewVScroll(cf.form),
		container.NewHBox(layout.NewSpacer(), extra),
	))
}

// newTestConnectionButton builds the "Test" button that dials the form's
// connection and reports the server version, without saving anything.
func newTestConnectionButton(w fyne.Window, build func() Connection) *widget.Button {
	return widget.NewButton("Test", func() {
		c := build()
		progress := dialog.NewProgressInfinite("Testing", "Connecting to "+c.Host+":"+c.Port+"...", w)
		progress.Show()
		safeGo("dialog", func() {
			var server string
			sess, err := dialConn(c)
			if err == nil {
				server = sess.Server()
				sess.Close()
			}
			safeDo(func() {
				progress.Hide()
				if err != nil {
					dialog.NewError(err, w).Show()
					return
				}
				msg := "Connection OK"
				if server != "" {
					msg += "\n" + server
				}
				showInfo(w, "Test Connection", msg)
			})
		})
	})
}
