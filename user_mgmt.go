package main

// User management (TablePlus "User Management"): a light GUI wrapper around
// the server's user DDL — list users, create, drop, and grant database
// access — for MySQL/MariaDB and PostgreSQL. All statements are gated by the
// connection's Safe Mode before execution.

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// userRow is one database login as shown in the user list.
type userRow struct {
	user string
	host string // MySQL only; empty for PG
}

// openUserManagement dials the user list for the current connection and pops
// the management dialog. Unsupported drivers get a plain notice.
func (ws *workspace) openUserManagement() {
	if ws.sess == nil {
		return
	}
	switch ws.conn.Driver {
	case "mysql", "mariadb", "postgres", "postgresql":
	default:
		showInfo(ws.window, "User Management",
			"User management is supported on MySQL/MariaDB and PostgreSQL.")
		return
	}
	sess := ws.sess
	gen := ws.nextGen()
	safeGo("userlist", func() {
		rows, err := ws.fetchUserRows(sess)
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, "User Management", "Could not list users:\n"+err.Error())
				return
			}
			ws.showUserDialog(rows)
		})
	})
}

// fetchUserRows returns the login users for the connection's driver.
func (ws *workspace) fetchUserRows(sess db.Session) ([]userRow, error) {
	var q string
	one := func(row []*string) userRow {
		if len(row) > 0 && row[0] != nil {
			return userRow{user: *row[0]}
		}
		return userRow{}
	}
	two := func(row []*string) userRow {
		if len(row) > 1 && row[0] != nil {
			h := ""
			if row[1] != nil {
				h = *row[1]
			}
			return userRow{user: *row[0], host: h}
		}
		return userRow{}
	}
	var pick func([]*string) userRow
	switch ws.conn.Driver {
	case "mysql", "mariadb":
		q = "SELECT user, host FROM mysql.user ORDER BY user, host"
		pick = two
	default: // postgres
		q = "SELECT rolname FROM pg_roles WHERE rolcanlogin ORDER BY rolname"
		pick = one
	}
	res, err := sess.Query(q, 500)
	if err != nil {
		return nil, err
	}
	var out []userRow
	for _, r := range res.Rows {
		if u := pick(r); u.user != "" {
			out = append(out, u)
		}
	}
	return out, nil
}

// showUserDialog is the main user-management panel: list + actions.
func (ws *workspace) showUserDialog(users []userRow) {
	isMySQL := ws.conn.Driver == "mysql" || ws.conn.Driver == "mariadb"

	names := make([]string, len(users))
	for i, u := range users {
		if isMySQL && u.host != "" {
			names[i] = u.user + "@" + u.host
		} else {
			names[i] = u.user
		}
	}
	list := widget.NewList(
		func() int { return len(names) },
		func() fyne.CanvasObject { return widget.NewLabel("user") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < len(names) {
				obj.(*widget.Label).SetText(names[id])
			}
		},
	)

	selIdx := -1
	list.OnSelected = func(id widget.ListItemID) { selIdx = int(id) }
	list.OnUnselected = func(widget.ListItemID) { selIdx = -1 }

	selected := func() *userRow {
		if selIdx < 0 || selIdx >= len(users) {
			return nil
		}
		return &users[selIdx]
	}

	// Execute one user DDL statement (Safe Mode gated), then refresh the list.
	runUserDDL := func(title, stmt string) {
		gateSafeMode(ws.window, ws.conn, stmt, func() { ws.execUserDDL(title, stmt, &users, &names, isMySQL, list) })
	}

	newBtn := widget.NewButton("New user…", func() {
		ws.promptNewUser(ws.conn.Driver, func(p userPrompt) {
			host := p.host
			if host == "" {
				host = "%"
			}
			if isMySQL {
				runUserDDL("New user",
					fmt.Sprintf("CREATE USER '%s'@'%s' IDENTIFIED BY '%s'",
						strings.ReplaceAll(p.user, "'", "''"),
						strings.ReplaceAll(host, "'", "''"),
						strings.ReplaceAll(p.password, "'", "''")))
			} else {
				runUserDDL("New user",
					fmt.Sprintf(`CREATE USER "%s" WITH PASSWORD '%s'`,
						strings.ReplaceAll(p.user, `"`, `""`),
						strings.ReplaceAll(p.password, "'", "''")))
			}
		})
	})

	delBtn := widget.NewButton("Delete selected", func() {
		u := selected()
		if u == nil {
			showInfo(ws.window, "Delete user", "Select a user first.")
			return
		}
		host := u.host
		if host == "" {
			host = "%"
		}
		if isMySQL {
			runUserDDL("Delete user", fmt.Sprintf("DROP USER '%s'@'%s'",
				strings.ReplaceAll(u.user, "'", "''"), strings.ReplaceAll(host, "'", "''")))
		} else {
			runUserDDL("Delete user", fmt.Sprintf(`DROP USER "%s"`,
				strings.ReplaceAll(u.user, `"`, `""`)))
		}
	})

	grantBtn := widget.NewButton("Grant database access", func() {
		u := selected()
		if u == nil {
			showInfo(ws.window, "Grant", "Select a user first.")
			return
		}
		if ws.currentDB == "" {
			showInfo(ws.window, "Grant", "Pick a database first (Ctrl+K).")
			return
		}
		if isMySQL {
			runUserDDL("Grant", fmt.Sprintf("GRANT ALL PRIVILEGES ON %s.* TO '%s'@'%s'",
				ws.quoteTable(ws.currentDB),
				strings.ReplaceAll(u.user, "'", "''"),
				strings.ReplaceAll(u.host, "'", "''")))
		} else {
			runUserDDL("Grant", fmt.Sprintf(`GRANT ALL ON DATABASE %s TO "%s"`,
				ws.quoteTable(ws.currentDB), strings.ReplaceAll(u.user, `"`, `""`)))
		}
	})

	body := container.NewBorder(
		widget.NewLabel(fmt.Sprintf("%d user(s). Supported: create / delete / grant on the current database.", len(users))),
		container.NewHBox(newBtn, spacerW(8), delBtn, spacerW(8), grantBtn),
		nil, nil,
		container.NewScroll(container.NewPadded(list)),
	)
	d := dialog.NewCustom("User Management — "+ws.conn.Name, "", body, ws.window)
	d.Resize(fyne.NewSize(460, 420))
	d.Show()
}

// execUserDDL runs one user DDL statement that passed the Safe Mode gate and
// refreshes the dialog's user list in place.
func (ws *workspace) execUserDDL(title, stmt string, users *[]userRow, names *[]string, isMySQL bool, list *widget.List) {
	gen := ws.nextGen()
	safeGo("userddl "+title, func() {
		_, err := ws.sess.Query(stmt, 1)
		safeDo(func() {
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, title, "Failed:\n"+err.Error())
				return
			}
			if refresh, ferr := ws.fetchUserRows(ws.sess); ferr == nil {
				*users = refresh
				*names = (*names)[:0]
				for _, u := range *users {
					if isMySQL && u.host != "" {
						*names = append(*names, u.user+"@"+u.host)
					} else {
						*names = append(*names, u.user)
					}
				}
				list.Refresh()
			}
			showInfo(ws.window, title, "Done.")
		})
	})
}

// userPrompt is the payload of the new-user dialog.
type userPrompt struct {
	user     string
	password string
	host     string
}

// promptNewUser asks for the new user's credentials, then invokes cb with
// the chosen values.
func (ws *workspace) promptNewUser(driver string, cb func(userPrompt)) {
	name := widget.NewEntry()
	name.SetPlaceHolder("username")
	pass := widget.NewPasswordEntry()
	pass.SetPlaceHolder("password")
	var host string
	var hostEntry *widget.Entry
	if driver == "mysql" || driver == "mariadb" {
		hostEntry = widget.NewEntry()
		hostEntry.SetPlaceHolder("host (% for any)")
		hostEntry.SetText("%")
	}

	var d dialog.Dialog
	ok := widget.NewButton("Create", func() {
		d.Hide()
		if hostEntry != nil {
			host = hostEntry.Text
		}
		cb(userPrompt{user: strings.TrimSpace(name.Text), password: pass.Text, host: host})
	})
	ok.Importance = widget.HighImportance
	cancel := widget.NewButton("Cancel", func() { d.Hide() })

	fields := container.NewVBox(
		widget.NewLabel("Username"), name,
		widget.NewLabel("Password"), pass,
	)
	if hostEntry != nil {
		fields.Objects = append(fields.Objects, widget.NewLabel("Host"), hostEntry)
	}
	d = dialog.NewCustom("New user", "",
		container.NewBorder(
			fields,
			container.NewHBox(container.NewBorder(nil, nil, nil, ok, cancel)),
			nil, nil, nil,
		), ws.window)
	d.Resize(fyne.NewSize(420, 300))
	d.Show()
}
