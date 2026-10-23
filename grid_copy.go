package main

// Right-click context menu on a data row (TablePlus "Copy Rows As"):
// copy the selected row / cell in several target formats, and generate a
// SQL IN-clause from one column's values.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// maxCopyRows caps "copy all rows" payloads so a big result set cannot flood
// the clipboard.
const maxCopyRows = 2000

// copyMenu pops the row context menu. The column under the pointer (when
// resolvable) gets the cell-level items; -1 means the pointer was on the
// gutter and only row-level items apply.
func (t *dataTable) copyMenu(row, col int, pos fyne.Position) {
	if t.window == nil {
		return
	}
	canvas := fyne.CurrentApp().Driver().CanvasForObject(t)
	if canvas == nil {
		return
	}

	var items []*fyne.MenuItem
	if col >= 0 && col < len(t.data.Cols) {
		title := t.colTitle(col)
		items = append(items,
			fyne.NewMenuItem("Quick Look", func() { t.quickLook(row, col) }),
			fyne.NewMenuItem("Copy Cell Value", func() {
				t.copyText(cellPlain(rowValues(t, row), col))
			}),
			fyne.NewMenuItem("Generate SQL IN List for "+title, func() {
				t.copyText(t.inClause(col))
			}),
			fyne.NewMenuItemSeparator(),
		)
	}
	items = append(items,
		fyne.NewMenuItem("Copy Row as CSV", func() { t.copyText(t.rowCSV(row, true)) }),
		fyne.NewMenuItem("Copy Row as JSON", func() { t.copyText(t.rowJSON(row)) }),
		fyne.NewMenuItem("Copy Row as Markdown", func() { t.copyText(t.rowMarkdown(row)) }),
		fyne.NewMenuItem("Copy Row as SQL INSERT", func() { t.copyText(t.rowInsert(row)) }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Copy All Rows as CSV (with header)", func() { t.copyText(t.allCSV()) }),
		fyne.NewMenuItem("Copy All Rows as SQL INSERTs", func() { t.copyText(t.allInserts()) }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Export All Rows to File…", func() { t.exportRows(t.window) }),
	)
	if t.onPin != nil {
		items = append(items, fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Pin this result", func() { t.onPin() }))
	}
	widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), canvas, pos)
}

// copyText pushes a string onto the system clipboard.
func (t *dataTable) copyText(s string) {
	if t.window == nil {
		return
	}
	fyne.CurrentApp().Clipboard().SetContent(s)
}

// rowValues returns one row's effective values (pending edits applied).
func rowValues(t *dataTable, row int) []*string {
	if row < 0 || row >= len(t.data.Rows) {
		return nil
	}
	return t.rowValues(row)
}

// cellPlain renders one cell for the clipboard (NULL as the word NULL).
func cellPlain(row []*string, col int) string {
	if col >= len(row) {
		return ""
	}
	if row[col] == nil {
		return "NULL"
	}
	return *row[col]
}

// csvField quotes a value for a CSV file (double-quotes, doubled inner quotes).
func csvField(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// rowCSV renders one row as a single CSV line (withHeader adds the header row
// above it).
func (t *dataTable) rowCSV(row int, withHeader bool) string {
	vals := t.rowValues(row)
	var b strings.Builder
	if withHeader {
		b.WriteString(strings.Join(t.headerNames(), ",") + "\n")
	}
	fields := make([]string, len(t.data.Cols))
	for i, c := range t.data.Cols {
		v := ""
		if i < len(vals) && vals[i] != nil {
			v = *vals[i]
		}
		fields[i] = csvField(v)
		_ = c
	}
	b.WriteString(strings.Join(fields, ",") + "\n")
	return b.String()
}

// headerNames returns the column titles, magic suffixes stripped.
func (t *dataTable) headerNames() []string {
	out := make([]string, len(t.data.Cols))
	for i, c := range t.data.Cols {
		out[i] = c.Name
	}
	return out
}

// rowJSON renders one row as a single JSON object ({"col": value}).
func (t *dataTable) rowJSON(row int) string {
	vals := t.rowValues(row)
	obj := map[string]any{}
	for i, c := range t.data.Cols {
		if i < len(vals) && vals[i] != nil {
			obj[c.Name] = *vals[i]
		}
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// rowMarkdown renders the header + one row as a GitHub markdown table.
func (t *dataTable) rowMarkdown(row int) string {
	vals := t.rowValues(row)
	head := make([]string, len(t.data.Cols))
	sep := make([]string, len(t.data.Cols))
	body := make([]string, len(t.data.Cols))
	for i, c := range t.data.Cols {
		head[i] = c.Name
		sep[i] = "---"
		if i < len(vals) && vals[i] != nil {
			body[i] = *vals[i]
		}
	}
	return "|" + strings.Join(head, "|") + "|\n" +
		"|" + strings.Join(sep, "|") + "|\n" +
		"|" + strings.Join(body, "|") + "|"
}

// rowInsert renders one row as an INSERT statement for this table (magic
// suffixes stripped from the column list; the grid's pending edits applied).
func (t *dataTable) rowInsert(row int) string {
	vals := t.rowValues(row)
	table := t.tableName
	if table == "" {
		table = "table"
	}
	cols := make([]string, 0, len(t.data.Cols))
	ph := make([]string, 0, len(t.data.Cols))
	for i, c := range t.data.Cols {
		base, _ := splitMagic(c.Name)
		cols = append(cols, base)
		ph = append(ph, sqlLiteral(literalOf(vals, i)))
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);",
		table, strings.Join(cols, ", "), strings.Join(ph, ", "))
}

// literalOf returns the cell's value for SQL rendering (nil passes through).
func literalOf(vals []*string, i int) *string {
	if i < len(vals) {
		return vals[i]
	}
	return nil
}

// allCSV renders every fetched row (up to maxCopyRows) with a header.
func (t *dataTable) allCSV() string {
	n := len(t.data.Rows)
	if n > maxCopyRows {
		n = maxCopyRows
	}
	var b strings.Builder
	b.WriteString(strings.Join(t.headerNames(), ",") + "\n")
	for r := 0; r < n; r++ {
		vals := t.rowValues(r)
		fields := make([]string, len(t.data.Cols))
		for i := range t.data.Cols {
			fields[i] = csvField(cellPlain(vals, i))
		}
		b.WriteString(strings.Join(fields, ",") + "\n")
	}
	return b.String()
}

// allJSON renders every fetched row (up to maxCopyRows) as a JSON array of
// objects ({"col": value, ...}); a SQL NULL becomes JSON null.
func (t *dataTable) allJSON() string {
	n := len(t.data.Rows)
	if n > maxCopyRows {
		n = maxCopyRows
	}
	rows := make([]map[string]interface{}, 0, n)
	for r := 0; r < n; r++ {
		vals := t.rowValues(r)
		obj := make(map[string]interface{}, len(t.data.Cols))
		for i, c := range t.data.Cols {
			var v *string
			if i < len(vals) {
				v = vals[i]
			}
			if v == nil {
				obj[c.Name] = nil
			} else {
				obj[c.Name] = *v
			}
		}
		rows = append(rows, obj)
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(b)
}

// allInserts renders every fetched row as INSERT statements.
func (t *dataTable) allInserts() string {
	n := len(t.data.Rows)
	if n > maxCopyRows {
		n = maxCopyRows
	}
	var b strings.Builder
	for r := 0; r < n; r++ {
		b.WriteString(t.rowInsert(r) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// inClause builds "col IN (…)" from the distinct values of one column across
// all fetched rows (NULLs skipped, up to maxCopyRows values). Numeric columns
// stay unquoted; everything else is single-quoted.
func (t *dataTable) inClause(col int) string {
	base, _ := splitMagic(t.data.Cols[col].Name)
	numeric := isNumericType(t.data.Cols[col].Type)
	seen := map[string]bool{}
	var vals []string
	for r := 0; r < len(t.data.Rows) && len(vals) < maxCopyRows; r++ {
		v := cellStr(t.data.Rows[r], col)
		if v == nil {
			continue
		}
		if seen[*v] {
			continue
		}
		seen[*v] = true
		if numeric {
			if _, err := strconv.ParseFloat(*v, 64); err == nil {
				vals = append(vals, *v)
				continue
			}
		}
		vals = append(vals, sqlLiteral(v))
	}
	return base + " IN (" + strings.Join(vals, ", ") + ")"
}
