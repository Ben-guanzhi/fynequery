package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

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
		if err := f.validate(); err != nil {
			dialog.ShowInformation("无法保存", err.Error(), c.win)
			return
		}
		cfg := f.config()
		cfg.Options = map[string]string{}
		refilter := false
		if edit != nil {
			cfg.ID = edit.ID
			// A filter only shows up in the navigator, so reloading the tree is what
			// separates "applies as soon as you save" from a change that looks inert
			// until the next reconnect. Other edits leave the tree alone - fixing a
			// typo in a name should not collapse one the user carefully expanded.
			refilter = filtersDiffer(edit.DatabaseFilter, cfg.DatabaseFilter) && c.store.ConnectionStatus(cfg.ID)
			if err := c.store.UpdateConnection(cfg); err != nil {
				dialog.ShowError(err, c.win)
				return
			}
		} else {
			c.store.AddConnection(cfg)
		}
		c.refreshConnections()
		if refilter {
			c.refreshSchema()
			c.dbSelRefresh()
		}
		if d != nil {
			d.Hide()
		}
	}

	test := func() {
		if err := f.validate(); err != nil {
			dialog.ShowInformation("无法测试", err.Error(), c.win)
			return
		}
		c.testAndReport(f.config())
	}

	saveBtn := widget.NewButtonWithIcon("保存", theme.ConfirmIcon(), save)
	testBtn := widget.NewButtonWithIcon("测试", theme.MailComposeIcon(), test)
	buttons := container.NewHBox(testBtn, layout.NewSpacer(), saveBtn)

	// What the form cannot show by itself gets said out loud at the bottom.
	title := "新建连接"
	if edit != nil {
		title = "编辑连接"
	}
	content := connDialogContent(f, connNotes(db.SecretsSealed()), buttons)
	d = dialog.NewCustom(title, "取消", content, c.win)
	d.Resize(connDialogSize)
	d.Show()
}

// connDialogContent stacks the form over its notes and button row. It is a
// function rather than a few inline lines so the size assertion in
// TestConnDialogShowsAll measures the object the dialog actually shows.
func connDialogContent(f *connFields, notes []fyne.CanvasObject, buttons fyne.CanvasObject) fyne.CanvasObject {
	return container.NewBorder(nil, container.NewVBox(append(notes, buttons)...), nil, nil, f.form())
}

// connDialogSize is the create/edit dialog's opening size. A custom dialog does
// not grow to its content, so this number has to clear the form plus the notes
// and buttons stacked under it - TestConnDialogShowsAll keeps it honest.
var connDialogSize = fyne.NewSize(470, 600)

// connNotes lists what the form cannot show by itself: a platform that cannot
// seal a password must not let the user assume 保存 encrypts it, and the
// filter's mask syntax is not guessable.
func connNotes(sealed bool) []fyne.CanvasObject {
	notes := []fyne.CanvasObject{}
	if note := secretStorageNote(sealed); note != "" {
		notes = append(notes, noteLabel(note))
	}
	return append(notes, noteLabel(filterNote))
}

// noteLabel is one of the connection dialog's word-wrapped warnings.
func noteLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	l.Importance = widget.HighImportance
	return l
}

// filterNote is the one place the mask vocabulary is spelled out for the user.
const filterNote = "库过滤：每行（或逗号分隔）一个模式，% 和 * 匹配任意串、_ 匹配单个字符、不分大小写；" +
	"排除优先于包含，包含留空＝显示所有没被排除的库，两栏都空＝显示全部。" +
	"过滤只影响左侧结构树，SQL 仍然打得到整台服务器；SQLite 只有一个库，这项无效。"

// connFields bundles the create/edit form's controls. It is passed around by
// pointer because the engine-switch and 加密 closures mutate sslStored, so the
// dialog and the widgets must share one instance.
type connFields struct {
	nameE, hostE, portE, userE, passE, dbE *widget.Entry
	typeSel, sslSel                        *widget.Select
	// includeE and excludeE hold the navigator's database filter, one pattern per
	// line; they are disabled for SQLite, whose single database cannot be hidden.
	includeE, excludeE *widget.Entry
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
		// Two multi-line boxes rather than a pattern-list widget: the lists are
		// short, they round-trip to one line per pattern, and a box you can paste
		// into beats clicking an Add button once per database.
		includeE: widget.NewMultiLineEntry(),
		excludeE: widget.NewMultiLineEntry(),
	}
	f.includeE.PlaceHolder = "只显示这些库，如 APP*  /  *_env"
	f.excludeE.PlaceHolder = "隐藏这些库，如 tempdb  /  *_backup"
	f.includeE.SetMinRowsVisible(filterRows)
	f.excludeE.SetMinRowsVisible(filterRows)
	f.filePicker = widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		c.pickDatabaseFile(f.dbE)
	})
	f.filePicker.Importance = widget.LowImportance

	f.sslSel.OnChanged = func(label string) {
		f.sslStored = sslValueFor(sslChoicesFor(engineType(f.typeSel.Selected)), label)
	}

	// The three field checks read the 类型 dropdown live, so the closures
	// stay right after an engine switch; re-running them only repaints.
	f.hostE.Validator = f.hostValidator
	f.portE.Validator = f.portValidator
	f.dbE.Validator = f.dbValidator
	f.applyEngineDefaults(edit)

	// Friendly default ports per engine when creating a new connection.
	f.typeSel.OnChanged = func(t string) {
		f.updatePicker()
		// Each engine asks for its own encryption vocabulary.
		f.applyEngineSSL(f.sslStored)
		// The field rules moved with the engine (SQLite: no host, no port);
		// force a re-check so a carried-over value turns red instead of
		// waiting for the next keystroke.
		f.hostE.Validate()
		f.portE.Validate()
		f.dbE.Validate()
		if edit != nil {
			return
		}
		f.portE.SetText(defaultPortFor(engineType(t)))
	}
	f.updatePicker()
	return f
}

// applyEngineDefaults seeds the fields from an existing connection, or from
// the create-mode defaults. It runs while typeSel.OnChanged is still nil, so
// SetSelected here cannot repaint the form before the seed is complete.
func (f *connFields) applyEngineDefaults(edit *db.ConnConfig) {
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
		f.includeE.SetText(renderPatterns(edit.DatabaseFilter.Include))
		f.excludeE.SetText(renderPatterns(edit.DatabaseFilter.Exclude))
		f.applyEngineSSL(edit.SSLMode)
		return
	}
	f.typeSel.SetSelected("MySQL")
	f.portE.SetText(defaultPortFor(db.DbTypeMySQL))
	f.applyEngineSSL("")
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
		widget.NewFormItem("库过滤(包含)", f.includeE),
		widget.NewFormItem("库过滤(排除)", f.excludeE),
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

// updatePicker adjusts the rows whose meaning depends on the engine: the file
// picker only exists while one is being read, and a filter has nothing to hide
// for SQLite, whose sole database is the file itself.
func (f *connFields) updatePicker() {
	sqlite := engineType(f.typeSel.Selected) == db.DbTypeSQLite
	if sqlite {
		f.filePicker.Show()
	} else {
		f.filePicker.Hide()
	}
	for _, e := range []*widget.Entry{f.includeE, f.excludeE} {
		if sqlite {
			e.Disable()
		} else {
			e.Enable()
		}
	}
}

// isSQLite answers whether the form currently names a file engine, whose
// fields are the ones other engines' rules would get wrong (host unused,
// port absent, 数据库 = file path).
func (f *connFields) isSQLite() bool {
	return engineType(f.typeSel.Selected) == db.DbTypeSQLite
}

// hostValidator flags the one case a server connection cannot dial: an empty
// 主机. SQLite reads a file, so there the field is unused and never a fault.
func (f *connFields) hostValidator(s string) error {
	if f.isSQLite() {
		return nil
	}
	if strings.TrimSpace(s) == "" {
		return errors.New("主机不能为空")
	}
	return nil
}

// portValidator answers what is wrong with 端口 for the engine on screen:
// a server needs a number in range, and SQLite has no port at all - a value
// left over from a type switch is a fault there, not a default.
func (f *connFields) portValidator(s string) error {
	s = strings.TrimSpace(s)
	if f.isSQLite() {
		if s != "" {
			return errors.New("SQLite 没有端口，请清空此项")
		}
		return nil
	}
	if s == "" {
		return errors.New("端口不能为空")
	}
	if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 {
		return errors.New("端口需为 1–65535 的数字")
	}
	return nil
}

// dbValidator is the SQLite-only rule: a file engine's 数据库 field is the
// file itself, so it must name something the picker or the user filled in.
func (f *connFields) dbValidator(s string) error {
	if f.isSQLite() && strings.TrimSpace(s) == "" {
		return errors.New("SQLite 需要指定数据库文件路径")
	}
	return nil
}

// validate forces every field check to run and reports all current problems
// in one line: 保存 / 测试 must not fire on a half-typed form, and the
// per-field red text the entries just rendered is the where.
func (f *connFields) validate() error {
	var problems []string
	for _, e := range []*widget.Entry{f.hostE, f.portE, f.dbE} {
		if err := e.Validate(); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "；"))
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
		// The two boxes are text; the config keeps a list per side.
		DatabaseFilter: db.DatabaseFilter{
			Include: parsePatterns(f.includeE.Text),
			Exclude: parsePatterns(f.excludeE.Text),
		},
	}
	if cfg.Name == "" {
		cfg.Name = fmt.Sprintf("%s@%s", cfg.Username, cfg.Host)
	}
	return cfg
}

// filterRows is how tall the two filter boxes stand in the form. Two lines is
// enough to read back a short list without turning the dialog into a scroll.
const filterRows = 2

// parsePatterns reads a filter box into the patterns it lists. A line break or a
// comma separates two patterns - people type both, and a comma cannot appear in
// the kind of name these masks are written for. Blanks are dropped and the order
// typed is kept, so the box reads back the same way it was written.
func parsePatterns(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	}) {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// renderPatterns is parsePatterns' inverse: one pattern per line.
func renderPatterns(list []string) string {
	return strings.Join(list, "\n")
}

// filtersDiffer answers whether a saved connection's visible databases change.
func filtersDiffer(a, b db.DatabaseFilter) bool {
	return renderPatterns(a.Include) != renderPatterns(b.Include) ||
		renderPatterns(a.Exclude) != renderPatterns(b.Exclude)
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
