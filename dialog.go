package main

// "Create a new connection..." dialog styled after TablePlus's connection
// sheet: DB-type selector + host/port/user/password/database fields.
// Connect dials the database for real, saves the connection and opens the
// workspace; Test only verifies connectivity.

import (
	"fmt"

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

// showConnectionDialog opens the connection sheet. When seed is non-zero its
// fields pre-fill the form (used by "Import from URL"); otherwise the form
// starts on conventional defaults.
func showConnectionDialog(w fyne.Window, seed Connection, onConnect func(Connection, db.Session)) {
	// Type picker from the driver registry (MySQL builtin + plugins).
	infos := db.Drivers()
	typeNames := make([]string, len(infos))
	codeByName := map[string]string{}
	nameByCode := map[string]string{}
	defaultMySQL := "MySQL"
	for i, in := range infos {
		typeNames[i] = in.Name
		codeByName[in.Name] = in.Code
		nameByCode[in.Code] = in.Name
	}
	if len(typeNames) == 0 {
		dialog.NewError(fmt.Errorf("no database drivers registered"), w).Show()
		return
	}

	picker := widget.NewSelect(typeNames, nil)
	if _, ok := codeByName["MySQL"]; ok {
		picker.SetSelected(defaultMySQL)
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
		default: // mysql
			user.SetText("root")
			dbname.SetText("")
		}
	}
	setDefaults(picker.Selected)
	picker.OnChanged = setDefaults

	// Pre-fill from the seed (Import from URL), overriding the defaults above.
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

	form := widget.NewForm(
		widget.NewFormItem("Type", picker),
		widget.NewFormItem("Name", name),
		widget.NewFormItem("Host", host),
		widget.NewFormItem("Port", port),
		widget.NewFormItem("User", user),
		widget.NewFormItem("Password", pass),
		widget.NewFormItem("Database", dbname),
		widget.NewFormItem("Status Color", colorSel),
		widget.NewFormItem("Safe Mode", safeSel),
		widget.NewFormItem("Password Storage", keyringChk),
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
		}
	}

	test := widget.NewButton("Test", func() {
		c := buildConn()
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

	d := dialog.NewCustom("New Connection", "", container.NewVBox(
		container.NewHBox(layout.NewSpacer(), picker, layout.NewSpacer()),
		widget.NewSeparator(),
		form,
		container.NewHBox(layout.NewSpacer(), test),
	), w)

	form.OnSubmit = func() {
		c := buildConn()
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
				}
				d.Hide() // dismiss the form before opening the workspace
				if onConnect != nil {
					onConnect(c, sess)
				}
			})
		})
	}
	form.SubmitText = "Connect"
	form.CancelText = "Cancel"
	form.OnCancel = func() {}

	d.Resize(fyne.NewSize(440, 620))
	d.Show()
}
