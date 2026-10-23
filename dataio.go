package main

// Data import / export: save the current result set to a CSV/JSON/SQL file, and
// import rows from a CSV/JSON file into the open table. Export reuses the
// grid's existing renderers; import maps the file's columns onto the table's
// columns and inserts through the session's BatchUpdater (one atomic
// transaction) with a Safe Mode gate.

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// exportRows asks for a format, then a destination file, and writes the whole
// fetched result set there.
func (t *dataTable) exportRows(w fyne.Window) {
	if t.window == nil {
		return
	}
	formats := []string{"CSV", "JSON", "SQL INSERT"}
	sel := widget.NewSelect(formats, nil)
	sel.SetSelected("CSV")

	var dlg dialog.Dialog
	cancel := widget.NewButton("Cancel", func() { dlg.Hide() })
	next := widget.NewButton("Save…", func() {
		format := sel.Selected
		dlg.Hide()
		content, fileName, filter := t.exportContent(format)
		save := dialog.NewFileSave(func(wc fyne.URIWriteCloser, err error) {
			if err != nil {
				dialog.NewError(err, w).Show()
				return
			}
			if wc == nil {
				return
			}
			if _, werr := wc.Write([]byte(content)); werr != nil {
				dialog.NewError(werr, w).Show()
			}
			if cerr := wc.Close(); cerr != nil {
				dialog.NewError(cerr, w).Show()
			}
		}, w)
		save.SetFilter(filter)
		save.SetFileName(fileName)
		save.Show()
	})
	next.Importance = widget.HighImportance

	body := container.NewVBox(
		widget.NewLabel(fmt.Sprintf("Export %d fetched row(s) to a file", min(len(t.data.Rows), maxCopyRows))),
		widget.NewForm(widget.NewFormItem("Format", sel)),
		container.NewHBox(layout.NewSpacer(), cancel, next),
	)
	dlg = dialog.NewCustom("Export", "", body, w)
	dlg.Resize(fyne.NewSize(380, 180))
	dlg.Show()
}

// exportContent renders the result in the chosen format and returns the text,
// a default filename and the matching file filter.
func (t *dataTable) exportContent(format string) (content, fileName string, filter storage.FileFilter) {
	switch format {
	case "JSON":
		return t.allJSON(), "export.json", storage.NewExtensionFileFilter([]string{".json"})
	case "SQL INSERT":
		return t.allInserts(), "export.sql", storage.NewExtensionFileFilter([]string{".sql"})
	default:
		return t.allCSV(), "export.csv", storage.NewExtensionFileFilter([]string{".csv"})
	}
}

// importData imports rows from a CSV/JSON file into the currently open table.
func (ws *workspace) importData() {
	sess := ws.liveSession()
	if sess == nil || ws.tableName == "" {
		showInfo(ws.window, "Import Data", "Open a table first.")
		return
	}
	table := ws.tableName
	open := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.NewError(err, ws.window).Show()
			return
		}
		if rc == nil {
			return
		}
		name := ""
		if u := rc.URI(); u != nil {
			name = strings.ToLower(u.Name())
		}
		data, rerr := io.ReadAll(rc)
		rc.Close()
		if rerr != nil {
			dialog.NewError(rerr, ws.window).Show()
			return
		}
		rows, headers, perr := parseImport(data, name)
		if perr != nil {
			dialog.NewError(perr, ws.window).Show()
			return
		}
		if len(rows) == 0 {
			showInfo(ws.window, "Import Data", "No data rows found in the file.")
			return
		}
		ws.confirmImport(sess, table, headers, rows)
	}, ws.window)
	open.SetFilter(storage.NewExtensionFileFilter([]string{".csv", ".json"}))
	open.Show()
}

// parseImport reads CSV or JSON (auto-detected from the filename or a leading
// '[') into rows keyed by column header. Empty CSV fields and JSON nulls become
// SQL NULL (nil).
func parseImport(data []byte, name string) (rows []map[string]*string, headers []string, err error) {
	trimmed := strings.TrimSpace(string(data))
	isJSON := strings.HasSuffix(name, ".json") || strings.HasPrefix(trimmed, "[")
	if isJSON {
		var raw []map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, nil, fmt.Errorf("parse JSON: %w", err)
		}
		seen := map[string]bool{}
		for _, obj := range raw {
			row := make(map[string]*string, len(obj))
			for k, v := range obj {
				seen[k] = true
				row[k] = jsonValue(v)
			}
			rows = append(rows, row)
		}
		headers = make([]string, 0, len(seen))
		for k := range seen {
			headers = append(headers, k)
		}
		sort.Strings(headers)
		return rows, headers, nil
	}

	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1 // tolerate ragged rows
	recs, cerr := r.ReadAll()
	if cerr != nil {
		return nil, nil, fmt.Errorf("parse CSV: %w", cerr)
	}
	if len(recs) == 0 {
		return nil, nil, nil
	}
	headers = recs[0]
	for _, rec := range recs[1:] {
		row := make(map[string]*string, len(headers))
		for i, h := range headers {
			if i >= len(rec) {
				break
			}
			if rec[i] == "" {
				row[h] = nil
			} else {
				s := rec[i]
				row[h] = &s
			}
		}
		rows = append(rows, row)
	}
	return rows, headers, nil
}

// jsonValue converts a decoded JSON value into a *string cell (nil = NULL).
func jsonValue(v interface{}) *string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return &t
	case float64:
		s := strconv.FormatFloat(t, 'g', -1, 64)
		return &s
	case bool:
		s := strconv.FormatBool(t)
		return &s
	default:
		b, _ := json.Marshal(t)
		s := string(b)
		return &s
	}
}

// confirmImport resolves the file's headers against the table's columns, builds
// the insert rows and asks for confirmation before writing.
func (ws *workspace) confirmImport(sess db.Session, table string, headers []string, rows []map[string]*string) {
	safeGo("import.columns", func() {
		cols, err := sess.Columns(table)
		safeDo(func() {
			if err != nil {
				dialog.NewError(err, ws.window).Show()
				return
			}
			byLower := make(map[string]string, len(cols))
			for _, c := range cols {
				byLower[strings.ToLower(c.Name)] = c.Name
			}
			var matched, skipped []string
			for _, h := range headers {
				if _, ok := byLower[strings.ToLower(h)]; ok {
					matched = append(matched, h)
				} else {
					skipped = append(skipped, h)
				}
			}
			if len(matched) == 0 {
				showInfo(ws.window, "Import Data", "None of the file's columns match the table's columns.")
				return
			}
			inserts := make([]map[string]*string, 0, len(rows))
			for _, row := range rows {
				ins := make(map[string]*string, len(matched))
				for _, h := range matched {
					if v, ok := row[h]; ok {
						ins[byLower[strings.ToLower(h)]] = v
					}
				}
				inserts = append(inserts, ins)
			}
			ws.runImport(sess, table, matched, skipped, inserts)
		})
	})
}

// runImport confirms the row/column counts, gates on Safe Mode, then writes the
// rows atomically (BatchUpdater) or one-by-one (Updater).
func (ws *workspace) runImport(sess db.Session, table string, matched, skipped []string, inserts []map[string]*string) {
	msg := fmt.Sprintf("Import %d row(s) into %q?\nColumns: %s", len(inserts), table, strings.Join(matched, ", "))
	if len(skipped) > 0 {
		msg += fmt.Sprintf("\n\nIgnored (no matching column): %s", strings.Join(skipped, ", "))
	}
	msg += "\n\nEmpty fields are imported as NULL."
	dialog.NewConfirm("Import Data", msg, func(ok bool) {
		if !ok {
			return
		}
		gateSafeMode(ws.window, ws.conn, "IMPORT "+table, func() {
			progress := dialog.NewProgressInfinite("Import", "Inserting rows…", ws.window)
			progress.Show()
			safeGo("import", func() {
				derr := ws.applyImports(sess, table, inserts)
				safeDo(func() {
					progress.Hide()
					if derr != nil {
						dialog.NewError(derr, ws.window).Show()
						return
					}
					showInfo(ws.window, "Import", fmt.Sprintf("Imported %d row(s) into %q.", len(inserts), table))
					ws.reloadTable()
				})
			})
		})
	}, ws.window).Show()
}

// applyImports writes the rows through the session: one atomic batch when a
// BatchUpdater is available, otherwise row by row.
func (ws *workspace) applyImports(sess db.Session, table string, inserts []map[string]*string) error {
	if b, ok := db.AsBatchUpdater(sess); ok {
		return b.ApplyChanges(table, nil, nil, inserts, nil)
	}
	up, ok := db.AsUpdater(sess)
	if !ok {
		return fmt.Errorf("this connection does not support editing")
	}
	for _, ins := range inserts {
		if err := up.InsertRow(table, ins); err != nil {
			return err
		}
	}
	return nil
}
