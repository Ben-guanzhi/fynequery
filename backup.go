package main

// Backup / Restore (TablePlus "Backup…" / "Restore…"): a lightweight,
// mysqldump-style export of a MySQL/MariaDB database (DROP + CREATE + INSERT)
// and a script runner for restoring a .sql file. Both reuse the ordinary
// db.Session.Query path, so no driver-protocol change is needed.

import (
	"fynequery/proto"

	"fmt"
	"io"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/dbcore/dbcore/sqltext"

	"fynequery/db"
)

const (
	dumpRowLimit = 100000 // max rows dumped per table
	dumpBatch    = 200    // rows per multi-row INSERT
)

// showBackupDialog lets the user export a schema (and optionally its data) to
// a .sql file. Supports MySQL/MariaDB.
func showBackupDialog(w fyne.Window, c Connection, sess db.Session) {
	dbEntry := widget.NewEntry()
	dbEntry.SetPlaceHolder("database")
	dbEntry.SetText(c.Database)

	incChk := widget.NewCheck("Include data (INSERT statements)", nil)
	incChk.SetChecked(true)

	var dlg dialog.Dialog
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })
	backupBtn := widget.NewButton("Backup…", func() {
		database := strings.TrimSpace(dbEntry.Text)
		includeData := incChk.Checked
		dlg.Hide()

		save := dialog.NewFileSave(func(wc fyne.URIWriteCloser, err error) {
			if err != nil {
				dialog.NewError(err, w).Show()
				return
			}
			if wc == nil {
				return
			}
			progress := dialog.NewProgressInfinite("Backup", "Dumping "+c.Name+"…", w)
			progress.Show()
			safeGo("backup", func() {
				script, warnings, derr := dumpDatabase(sess, c.Driver, database, includeData)
				if derr == nil {
					_, derr = wc.Write([]byte(script))
				}
				if cerr := wc.Close(); derr == nil {
					derr = cerr
				}
				safeDo(func() {
					progress.Hide()
					if derr != nil {
						dialog.NewError(derr, w).Show()
						return
					}
					msg := fmt.Sprintf("Backup complete.\n%d bytes written.", len(script))
					if len(warnings) > 0 {
						msg += "\n\n" + strings.Join(warnings, "\n")
					}
					showInfo(w, "Backup", msg)
				})
			})
		}, w)
		save.SetFilter(storage.NewExtensionFileFilter([]string{".sql"}))
		save.SetFileName(defaultBackupName(c, database))
		save.Show()
	})
	backupBtn.Importance = widget.HighImportance

	body := container.NewVBox(
		widget.NewLabel(fmt.Sprintf("Backup “%s” (%s)", c.Name, dbTypeByDriver(c.Driver).Name)),
		widget.NewForm(
			widget.NewFormItem("Database", dbEntry),
			widget.NewFormItem("Options", incChk),
		),
		container.NewHBox(layout.NewSpacer(), cancelBtn, backupBtn),
	)

	dlg = dialog.NewCustom("Backup", "", body, w)
	dlg.Resize(fyne.NewSize(460, 230))
	dlg.Show()
}

// showRestoreDialog picks a .sql file and runs its statements against the
// connection.
func showRestoreDialog(w fyne.Window, c Connection, sess db.Session) {
	open := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.NewError(err, w).Show()
			return
		}
		if rc == nil {
			return
		}
		fname := "file"
		if u := rc.URI(); u != nil {
			fname = u.Name()
		}
		data, rerr := io.ReadAll(rc)
		rc.Close()
		if rerr != nil {
			dialog.NewError(rerr, w).Show()
			return
		}
		dialog.NewConfirm("Restore",
			fmt.Sprintf("Run %s against %q?\n\nThis executes every statement in the file and cannot be undone.", fname, c.Name),
			func(ok bool) {
				if !ok {
					return
				}
				progress := dialog.NewProgressInfinite("Restore", "Running SQL script…", w)
				progress.Show()
				safeGo("restore", func() {
					stmts, rerr := runSQLScript(sess, string(data))
					safeDo(func() {
						progress.Hide()
						if rerr != nil {
							dialog.NewError(rerr, w).Show()
							return
						}
						showInfo(w, "Restore", fmt.Sprintf("Restore complete.\n%d statements executed.", stmts))
					})
				})
			}, w).Show()
	}, w)
	open.SetTitleText("Restore “" + c.Name + "” — open SQL file")
	open.SetFilter(storage.NewExtensionFileFilter([]string{".sql"}))
	open.Show()
}

// defaultBackupName builds a sensible default filename for a dump.
func defaultBackupName(c Connection, database string) string {
	base := database
	if base == "" {
		base = c.Name
	}
	if base == "" {
		base = "backup"
	}
	base = strings.Map(func(r rune) rune {
		if r == ' ' || r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, base)
	return base + ".sql"
}

// dumpDatabase renders a SQL script for the given database: for each base
// table a DROP+CREATE, then (optionally) batched INSERTs. It returns the
// script plus any warnings about data that could not be fully captured
// (tables that hit the per-query row cap are flagged instead of silently
// truncating the backup).
func dumpDatabase(sess db.Session, driver, database string, includeData bool) (string, []string, error) {
	switch driver {
	case "mysql", "mariadb":
	default:
		return "", nil, fmt.Errorf("backup currently supports MySQL/MariaDB only (this connection is %s)",
			dbTypeByDriver(driver).Name)
	}
	if sess == nil {
		return "", nil, fmt.Errorf("no active connection")
	}
	var warnings []string

	var b strings.Builder
	b.WriteString("-- fynequery backup\n")
	if database != "" {
		b.WriteString("-- database: " + database + "\n")
	}
	b.WriteString("SET FOREIGN_KEY_CHECKS=0;\n\n")

	showTables := "SHOW FULL TABLES"
	if database != "" {
		showTables = "SHOW FULL TABLES FROM " + quoteIdent(driver, database)
	}
	res, err := sess.Query(showTables, 10000)
	if err != nil {
		return "", nil, err
	}

	var tables []string
	for _, row := range res.StringRows() {
		if len(row) == 0 || row[0] == nil {
			continue
		}
		if len(row) > 1 && row[1] != nil && strings.EqualFold(*row[1], "VIEW") {
			continue
		}
		tables = append(tables, *row[0])
	}

	for _, t := range tables {
		full := quoteIdent(driver, t)
		if database != "" {
			full = quoteIdent(driver, database) + "." + full
		}

		if cr, err := sess.Query("SHOW CREATE TABLE "+full, 10); err == nil &&
			len(cr.Rows) > 0 && len(cr.Rows[0]) > 1 && cr.Rows[0][1].Kind != "null" {
			b.WriteString("DROP TABLE IF EXISTS " + quoteIdent(driver, t) + ";\n")
			b.WriteString(proto.CellText(cr.Rows[0][1]) + ";\n\n")
		}

		if !includeData {
			continue
		}
		data, err := sess.Query("SELECT * FROM "+full, dumpRowLimit)
		if err != nil {
			return "", nil, fmt.Errorf("reading %s: %w", t, err)
		}
		if len(data.Rows) == 0 {
			continue
		}
		if len(data.Rows) >= dumpRowLimit {
			warn := fmt.Sprintf("table %q reached the %d-row dump cap; its backup is incomplete —\nre-run with a smaller dataset or dump fewer tables.", t, dumpRowLimit)
			warnings = append(warnings, warn)
			b.WriteString("-- WARNING: " + t + " row dump hit the " + strconv.Itoa(dumpRowLimit) +
				" cap; backup for this table is INCOMPLETE\n")
		}
		cols := make([]string, len(data.Columns))
		for i, cn := range data.Columns {
			cols[i] = quoteIdent(driver, cn.Name)
		}
		colList := "(" + strings.Join(cols, ", ") + ")"
		for start := 0; start < len(data.Rows); start += dumpBatch {
			end := start + dumpBatch
			if end > len(data.Rows) {
				end = len(data.Rows)
			}
			b.WriteString("INSERT INTO " + quoteIdent(driver, t) + " " + colList + " VALUES\n")
			for i := start; i < end; i++ {
				b.WriteString("  (" + joinMySQLValues(proto.QueryResult{Rows: [][]proto.CellPayload{data.Rows[i]}}.StringRows()[0]) + ")")
				if i == end-1 {
					b.WriteString(";\n")
				} else {
					b.WriteString(",\n")
				}
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("SET FOREIGN_KEY_CHECKS=1;\n")
	return b.String(), warnings, nil
}

// runSQLScript executes a SQL script statement by statement, returning how
// many statements ran before an error (if any).
func runSQLScript(sess db.Session, script string) (int, error) {
	if sess == nil {
		return 0, fmt.Errorf("no active connection")
	}
	n := 0
	for _, stmt := range splitStatementsDelimited(script) {
		if _, err := sess.Query(stmt, 10); err != nil {
			return n, fmt.Errorf("statement %d failed: %w", n+1, err)
		}
		n++
	}
	return n, nil
}

// splitStatementsDelimited splits a SQL script into statements, honoring
// DELIMITER directives (as mysqldump emits around stored routines/triggers).
// Semicolons inside BEGIN…END blocks are kept intact until the active
// delimiter appears, while string literals, quoted identifiers and -- line
// comments are respected so a delimiter inside them does not split.
func splitStatementsDelimited(script string) []string {
	var out []string
	var sb strings.Builder
	delim := ";"
	var quote byte  // 0, '\'', '"' or '`'
	atStart := true // sb holds only whitespace so far (statement boundary)

	flush := func() {
		if s := strings.TrimSpace(sb.String()); s != "" {
			out = append(out, s)
		}
		sb.Reset()
		atStart = true
	}

	i, n := 0, len(script)
	for i < n {
		// At a statement start look for a DELIMITER directive before anything
		// else, so routine bodies that use the custom delimiter are kept whole.
		if atStart {
			if d, consumed, ok := readDelimiterDirective(script[i:]); ok {
				flush()
				delim = d
				i += consumed
				continue
			}
		}

		c := script[i]
		if quote != 0 {
			sb.WriteByte(c)
			if c == '\\' && quote != '`' && i+1 < n {
				sb.WriteByte(script[i+1])
				i += 2
				continue
			}
			if c == quote {
				quote = 0
			}
			i++
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			sb.WriteByte(c)
			atStart = false
		case '-':
			if i+1 < n && script[i+1] == '-' {
				for i < n && script[i] != '\n' {
					i++
				}
				sb.WriteByte('\n')
				continue
			}
			sb.WriteByte(c)
			atStart = false
		default:
			if strings.HasPrefix(script[i:], delim) {
				flush()
				i += len(delim)
				continue
			}
			sb.WriteByte(c)
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				atStart = false
			}
		}
		i++
	}
	flush()
	return out
}

// readDelimiterDirective parses a DELIMITER directive at the start of s and
// returns the delimiter token plus the number of bytes consumed (through the
// end of that directive line).
func readDelimiterDirective(s string) (string, int, bool) {
	if len(s) < 9 || !strings.EqualFold(s[:9], "DELIMITER") {
		return "", 0, false
	}
	after := 9
	if after >= len(s) || (s[after] != ' ' && s[after] != '\t' && s[after] != '\r' && s[after] != '\n') {
		return "", 0, false
	}
	for after < len(s) && (s[after] == ' ' || s[after] == '\t' || s[after] == '\r' || s[after] == '\n') {
		after++
	}
	start := after
	for after < len(s) && s[after] != ' ' && s[after] != '\t' && s[after] != '\r' && s[after] != '\n' {
		after++
	}
	if start == after {
		return "", 0, false
	}
	delim := s[start:after]
	for after < len(s) && s[after] != '\n' {
		after++
	}
	if after < len(s) {
		after++ // consume the newline
	}
	return delim, after, true
}

// splitStatements splits a SQL script on semicolons that sit outside string
// literals, quoted identifiers and -- line comments.
func splitStatements(script string) []string {
	var out []string
	var sb strings.Builder
	var quote byte // 0, '\'', '"' or '`'

	flush := func() {
		if s := strings.TrimSpace(sb.String()); s != "" {
			out = append(out, s)
		}
		sb.Reset()
	}

	for i := 0; i < len(script); i++ {
		c := script[i]
		if quote != 0 {
			sb.WriteByte(c)
			if c == '\\' && quote != '`' && i+1 < len(script) {
				sb.WriteByte(script[i+1])
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			sb.WriteByte(c)
		case '-':
			if i+1 < len(script) && script[i+1] == '-' {
				for i < len(script) && script[i] != '\n' {
					i++
				}
				sb.WriteByte('\n')
				continue
			}
			sb.WriteByte(c)
		case ';':
			flush()
		default:
			sb.WriteByte(c)
		}
	}
	flush()
	return out
}

// quoteIdent quotes an identifier for the driver.
func quoteIdent(driver, name string) string {
	if q, err := sqltext.QuoteIdent(driver, name); err == nil {
		return q
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// joinMySQLValues renders one row as a MySQL VALUES tuple.
func joinMySQLValues(row []*string) string {
	parts := make([]string, len(row))
	for i, v := range row {
		if v == nil {
			parts[i] = "NULL"
			continue
		}
		s := strings.ReplaceAll(*v, `\`, `\\`)
		s = strings.ReplaceAll(s, "'", "''")
		parts[i] = "'" + s + "'"
	}
	return strings.Join(parts, ", ")
}
