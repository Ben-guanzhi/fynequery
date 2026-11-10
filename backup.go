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
	"fynequery/internal/sqlscan"
)

// 可注入的包级变量而非 const：测试需要缩小行上限，才能触达 cap 截断与
// 文内 INCOMPLETE 标记路径（10 万行的真实 cap 在单测里够不着）。
var (
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
				// The dump streams straight into the file: the script never
				// sits in memory as one string (a mysqldump-sized export used
				// to hold the whole text plus its []byte copy).
				n, warnings, derr := dumpDatabase(sess, c.Driver, database, includeData, wc)
				if cerr := wc.Close(); derr == nil {
					derr = cerr
				}
				safeDo(func() {
					progress.Hide()
					if derr != nil {
						dialog.NewError(derr, w).Show()
						return
					}
					msg := fmt.Sprintf("Backup complete.\n%d bytes written.", n)
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
		// The read moves to a worker: a mysqldump-sized .sql read
		// synchronously in the callback froze the window (and string(data)
		// doubled the residency before the parse even started).
		safeGo("restoreRead", func() {
			data, rerr := io.ReadAll(rc)
			rc.Close()
			if rerr != nil {
				safeDo(func() { dialog.NewError(rerr, w).Show() })
				return
			}
			sqlText := string(data)
			data = nil
			safeDo(func() {
				dialog.NewConfirm("Restore",
					fmt.Sprintf("Run %s against %q?\n\nThis executes every statement in the file and cannot be undone.", fname, c.Name),
					func(ok bool) {
						if !ok {
							return
						}
						progress := dialog.NewProgressInfinite("Restore", "Running SQL script…", w)
						progress.Show()
						safeGo("restore", func() {
							stmts, rerr := runSQLScript(sess, sqlText)
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
			})
		})
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
// table a DROP+CREATE, then (optionally) batched INSERTs. The script streams
// to w as it is produced — the writer never holds the whole dump, and each
// table's data arrives through Streamer.QueryStream (batches of dumpBatch
// rows) instead of one materialized 100k-row result, so memory stays bounded
// by one batch per table rather than the full database. Returns the bytes
// written plus any warnings about data that could not be fully captured
// (tables that hit the per-query row cap are flagged instead of silently
// truncating the backup).
//
// Sessions without stream support (the Streamer capability is optional) fall
// back to the materialized path for data, at the old memory cost — the dump
// still works, it just is not bounded.
func dumpDatabase(sess db.Session, driver, database string, includeData bool, w io.Writer) (int64, []string, error) {
	switch driver {
	case "mysql", "mariadb":
	default:
		return 0, nil, fmt.Errorf("backup currently supports MySQL/MariaDB only (this connection is %s)",
			dbTypeByDriver(driver).Name)
	}
	if sess == nil {
		return 0, nil, fmt.Errorf("no active connection")
	}
	var warnings []string
	var total int64
	write := func(s string) error {
		n, err := io.WriteString(w, s)
		total += int64(n)
		return err
	}

	if err := write("-- fynequery backup\n"); err != nil {
		return total, warnings, err
	}
	if database != "" {
		if err := write("-- database: " + database + "\n"); err != nil {
			return total, warnings, err
		}
	}
	if err := write("SET FOREIGN_KEY_CHECKS=0;\n\n"); err != nil {
		return total, warnings, err
	}

	showTables := "SHOW FULL TABLES"
	if database != "" {
		showTables = "SHOW FULL TABLES FROM " + quoteIdent(driver, database)
	}
	res, err := sess.Query(showTables, 10000)
	if err != nil {
		return total, warnings, err
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

	streamer, canStream := sess.(db.Streamer)

	for _, t := range tables {
		full := quoteIdent(driver, t)
		if database != "" {
			full = quoteIdent(driver, database) + "." + full
		}

		// A failed SHOW CREATE must be visible: silently skipping it left the
		// dump without this table's DDL, so a restore could not recreate it.
		if cr, err := sess.Query("SHOW CREATE TABLE "+full, 10); err != nil {
			warnings = append(warnings, fmt.Sprintf("table %q: SHOW CREATE TABLE 失败，DDL 未写入备份：%v", t, err))
		} else if len(cr.Rows) > 0 && len(cr.Rows[0]) > 1 && cr.Rows[0][1].Kind != "null" {
			if err := write("DROP TABLE IF EXISTS " + quoteIdent(driver, t) + ";\n" +
				proto.CellText(cr.Rows[0][1]) + ";\n\n"); err != nil {
				return total, warnings, err
			}
		} else {
			warnings = append(warnings, fmt.Sprintf("table %q: SHOW CREATE TABLE 返回空，DDL 未写入备份", t))
		}

		if !includeData {
			continue
		}
		if canStream {
			if err := dumpTableDataStreaming(streamer, driver, t, full, write, &warnings); err != nil {
				return total, warnings, err
			}
			continue
		}
		if err := dumpTableDataMaterialized(sess, driver, t, full, write, &warnings); err != nil {
			return total, warnings, err
		}
	}

	if err := write("SET FOREIGN_KEY_CHECKS=1;\n"); err != nil {
		return total, warnings, err
	}
	return total, warnings, nil
}

// dumpTableDataStreaming writes one table's INSERT statements as the rows
// stream in. Each emit call carries up to dumpBatch rows and is flushed as
// ONE self-contained multi-row INSERT (mysqldump's extended-insert segments
// are the same shape): the column list arrives on the FIRST frame together
// with the first batch of rows (StreamRunCtx emits cols and the pending chunk
// in one flush — a "cols-only frame" never happens), tuples are joined with
// ",\n", and the statement ends with ";\n" decided at
// flush time — no written text ever needs patching. Peak memory is one batch
// per table, not the whole table. A client-side row cap (dumpRowLimit) stops
// the stream and flags the table INCOMPLETE instead of writing an arbitrarily
// truncated or unbounded script. An empty table writes no INSERT at all,
// matching the materialized path.
func dumpTableDataStreaming(streamer db.Streamer, driver, table, full string, write func(string) error, warnings *[]string) error {
	quoted := quoteIdent(driver, table)
	var colList string // " (`id`, `name`)" from the metadata call
	rowsWritten := 0
	stopped := false
	// The truncation must be visible in the dump itself, not just in the
	// completion dialog's warnings. The cap almost always lands on a batch
	// boundary (dumpRowLimit % dumpBatch == 0), so the room<=0 branch below
	// is the one that fires — this note is what marks the table INCOMPLETE
	// in the restored file.
	incomplete := "-- WARNING: " + table + " row dump hit the " + strconv.Itoa(dumpRowLimit) +
		" cap; backup for this table is INCOMPLETE\n"

	_, err := streamer.QueryStream("SELECT * FROM "+full, dumpBatch,
		func(cols []proto.ColumnInfo, rows [][]*string) (bool, error) {
			if cols != nil {
				names := make([]string, len(cols))
				for i, c := range cols {
					names[i] = quoteIdent(driver, c.Name)
				}
				colList = " (" + strings.Join(names, ", ") + ")"
				// cols shares its frame with the first batch of rows — fall
				// through and write that batch. Treating a cols frame as
				// metadata-only silently dropped the first dumpBatch rows of
				// every non-empty table.
			}
			if stopped || len(rows) == 0 {
				return !stopped, nil
			}
			// Enforce the per-table row cap client-side: take only what fits,
			// then stop the stream and flag the table INCOMPLETE.
			room := dumpRowLimit - rowsWritten
			if room <= 0 {
				*warnings = append(*warnings, fmt.Sprintf(
					"table %q reached the %d-row dump cap; its backup is incomplete —\nre-run with a smaller dataset or dump fewer rows.", table, dumpRowLimit))
				stopped = true
				if err := write(incomplete + "\n"); err != nil {
					return false, err
				}
				return false, nil
			}
			take := rows
			if len(rows) > room {
				take = rows[:room]
				*warnings = append(*warnings, fmt.Sprintf(
					"table %q reached the %d-row dump cap; its backup is incomplete —\nre-run with a smaller dataset or dump fewer rows.", table, dumpRowLimit))
				stopped = true
			}

			var b strings.Builder
			b.WriteString("INSERT INTO " + quoted + colList + " VALUES\n")
			for i, row := range take {
				b.WriteString("  (" + joinMySQLValues(row) + ")")
				if i < len(take)-1 {
					b.WriteString(",\n")
				}
			}
			b.WriteString(";\n")
			if stopped {
				b.WriteString(incomplete)
			}
			b.WriteString("\n")
			if err := write(b.String()); err != nil {
				return false, err
			}
			rowsWritten += len(take)
			return !stopped, nil
		})
	return err
}

// dumpTableDataMaterialized is the no-Streamer fallback: one materialized
// SELECT (the old path's memory cost), then batched INSERTs through the same
// writer. A session without stream support still gets a correct dump — it
// just is not memory-bounded.
func dumpTableDataMaterialized(sess db.Session, driver, table, full string, write func(string) error, warnings *[]string) error {
	data, err := sess.Query("SELECT * FROM "+full, dumpRowLimit)
	if err != nil {
		return fmt.Errorf("reading %s: %w", table, err)
	}
	if len(data.Rows) == 0 {
		return nil
	}
	if len(data.Rows) >= dumpRowLimit {
		*warnings = append(*warnings, fmt.Sprintf(
			"table %q reached the %d-row dump cap; its backup is incomplete —\nre-run with a smaller dataset or dump fewer rows.", table, dumpRowLimit))
	}
	names := make([]string, len(data.Columns))
	for i, cn := range data.Columns {
		names[i] = quoteIdent(driver, cn.Name)
	}
	colList := " (" + strings.Join(names, ", ") + ")"
	quoted := quoteIdent(driver, table)
	// Convert every cell to its display string once per table. Building a
	// one-row throwaway QueryResult per row (StringRows()[0]) allocated a
	// fresh payload per dumped row — a hundred thousand rows meant a
	// hundred thousand temporary structures.
	rows := data.StringRows()
	for start := 0; start < len(rows); start += dumpBatch {
		end := start + dumpBatch
		if end > len(rows) {
			end = len(rows)
		}
		var b strings.Builder
		b.WriteString("INSERT INTO " + quoted + colList + " VALUES\n")
		for i := start; i < end; i++ {
			b.WriteString("  (" + joinMySQLValues(rows[i]) + ")")
			if i == end-1 {
				b.WriteString(";\n")
			} else {
				b.WriteString(",\n")
			}
		}
		b.WriteString("\n")
		if err := write(b.String()); err != nil {
			return err
		}
	}
	return nil
}

// runSQLScript executes a SQL script statement by statement, returning how
// many statements ran before an error (if any).
func runSQLScript(sess db.Session, script string) (int, error) {
	if sess == nil {
		return 0, fmt.Errorf("no active connection")
	}
	n := 0
	for _, stmt := range sqlscan.SplitDelimited(script) {
		if _, err := sess.Query(stmt, 10); err != nil {
			return n, fmt.Errorf("statement %d failed: %w", n+1, err)
		}
		n++
	}
	return n, nil
}

// quoteIdent quotes an identifier for the driver.
//
// QuoteIdent rejects empty / overlong / control-carrying names. A backup still
// has to render whatever the catalog returned, so the fallback must keep the
// statement syntactically an identifier — and MySQL's ANSI double quotes are a
// string literal, which would silently turn `"db".table` into a nonsense
// expression instead of an error. sqltext.QuoteIdentLoose quotes with each
// dialect's own delimiters, so the fallback is dialect-correct.
func quoteIdent(driver, name string) string {
	if q, err := sqltext.QuoteIdent(driver, name); err == nil {
		return q
	}
	return sqltext.QuoteIdentLoose(driver, name)
}

// joinMySQLValues renders one row as a MySQL VALUES tuple.
//
// Two escapes are needed for MySQL's string grammar: a backslash starts an
// escape sequence (so it doubles) and a quote terminates the literal (so it
// doubles). NUL is the third: a raw 0x00 inside a text literal ends the value
// at that byte when the server parses the script, silently truncating the
// cell — and a dump is exactly where a BLOB-shaped cell (CellText renders
// []byte as a Go string) can carry one. `\0` is MySQL's escape for it.
//
// Time values arrive here already rendered by CellText, i.e. in the DISPLAY
// spelling ("YYYY-MM-DD HH:MM:SS[.fff]"), not the RFC3339 wire spelling. That
// happens to be exactly MySQL's timestamp literal grammar, which is why the
// dump restores correctly — but it is an implicit coupling: changing the
// display format (localized month names, a slash separator) would silently
// make every dumped timestamp unrestorable. If the display layer ever changes,
// this path needs its own renderer.
func joinMySQLValues(row []*string) string {
	parts := make([]string, len(row))
	for i, v := range row {
		if v == nil {
			parts[i] = "NULL"
			continue
		}
		s := strings.ReplaceAll(*v, `\`, `\\`)
		s = strings.ReplaceAll(s, "'", "''")
		s = strings.ReplaceAll(s, "\x00", `\0`)
		parts[i] = "'" + s + "'"
	}
	return strings.Join(parts, ", ")
}
