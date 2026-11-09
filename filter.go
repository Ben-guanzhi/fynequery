package main

// TablePlus-style filter sheet: pick a column / operator / value to build a
// WHERE clause, then reload the data view through the paging query path
// (SELECT * FROM t WHERE <clause> LIMIT ...). The raw WHERE entry is editable,
// so multi-condition filters can be typed directly.

import (
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	sqltext "github.com/dbcore/dbcore/sqltext"
)

var filterOperators = []string{
	"=", "!=", ">", ">=", "<", "<=", "LIKE", "NOT LIKE", "IN", "IS NULL", "IS NOT NULL",
}

// showFilterDialog opens the filter sheet for the currently open table.
func (ws *workspace) showFilterDialog() {
	if ws.tableName == "" {
		showInfo(ws.window, "Filter", "Open a table first.")
		return
	}
	// A filter is a WHERE clause, and only SQL drivers have one to offer: the
	// apply path builds SELECT … WHERE … LIMIT statements, which a Redis
	// session would parse as commands. Redis keys have no filter equivalent
	// (the value pager has no predicate), so the sheet refuses to open here.
	if !ws.pageSupported() {
		showInfo(ws.window, "Filter", "此连接类型不支持筛选（WHERE 仅 SQL 引擎可用）。")
		return
	}
	cols := ws.columnNames()
	if len(cols) == 0 {
		showInfo(ws.window, "Filter", "Column list is not available yet — wait for the table to load.")
		return
	}

	colSel := widget.NewSelect(cols, nil)
	opSel := widget.NewSelect(filterOperators, nil)
	valEntry := widget.NewEntry()
	valEntry.SetPlaceHolder("value")

	opSel.OnChanged = func(string) {
		switch opSel.Selected {
		case "IS NULL", "IS NOT NULL":
			valEntry.Disable()
		case "IN":
			valEntry.Enable()
			valEntry.SetPlaceHolder("1, 2, 3")
		default:
			valEntry.Enable()
			valEntry.SetPlaceHolder("value")
		}
	}
	opSel.SetSelected("=")
	colSel.SetSelected(cols[0])

	whereEntry := widget.NewEntry()
	whereEntry.SetPlaceHolder("column = value")
	if ws.filterSQL != "" {
		whereEntry.SetText(ws.filterSQL)
	}

	addBtn := widget.NewButton("Add condition", func() {
		cond := ws.buildCondition(colSel.Selected, opSel.Selected, valEntry.Text)
		if cond == "" {
			return
		}
		if cur := strings.TrimSpace(whereEntry.Text); cur != "" {
			whereEntry.SetText(cur + " AND " + cond)
		} else {
			whereEntry.SetText(cond)
		}
	})

	var dlg dialog.Dialog
	clearBtn := widget.NewButton("Clear", func() {
		ws.filterSQL = ""
		dlg.Hide()
		ws.applyFilter()
	})
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })
	applyBtn := widget.NewButton("Apply", func() {
		ws.filterSQL = strings.TrimSpace(whereEntry.Text)
		dlg.Hide()
		ws.applyFilter()
	})
	applyBtn.Importance = widget.HighImportance

	body := container.NewPadded(container.NewVBox(
		container.NewGridWithColumns(3, colSel, opSel, valEntry),
		container.NewHBox(layout.NewSpacer(), addBtn),
		spacerH(4),
		widget.NewLabelWithStyle("WHERE", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		whereEntry,
		container.NewHBox(layout.NewSpacer(), clearBtn, cancelBtn, applyBtn),
	))

	dlg = dialog.NewCustom("Filter", "", body, ws.window)
	dlg.Resize(fyne.NewSize(560, 220))
	dlg.Show()
}

// columnNames returns the open table's column names (from the loaded grid).
func (ws *workspace) columnNames() []string {
	if ws.grid == nil || ws.grid.data == nil {
		return nil
	}
	out := make([]string, 0, len(ws.grid.data.Cols))
	for _, c := range ws.grid.data.Cols {
		out = append(out, c.Name)
	}
	return out
}

// whereClause returns " WHERE <filter>" when a filter is active, else "".
func (ws *workspace) whereClause() string {
	if strings.TrimSpace(ws.filterSQL) == "" {
		return ""
	}
	return " WHERE " + ws.filterSQL
}

// refreshFilterChip marks the bottom-bar Filter button with a dot while a
// filter is active, so a narrowed result set is never a mystery.
func (ws *workspace) refreshFilterChip() {
	if ws.filterBtn == nil {
		return
	}
	if strings.TrimSpace(ws.filterSQL) == "" {
		ws.filterBtn.setLabel("Filter")
	} else {
		ws.filterBtn.setLabel("Filter ●")
	}
}

// applyFilter reloads the data view honoring the current filter. Clearing the
// filter on a driver without paging falls back to the plain table browse.
func (ws *workspace) applyFilter() {
	t := ws.activeData
	if t == nil || t.name == "" {
		return
	}
	// showFilterDialog refuses non-SQL drivers, so a filter cannot be set on
	// them; clearing one left over from a previous connection still has to
	// fall back to the plain browse rather than gotoOffset's SQL path.
	t.filterSQL = ws.filterSQL
	ws.refreshFilterChip()
	if strings.TrimSpace(ws.filterSQL) == "" && !ws.pageSupported() {
		ws.reloadActiveData()
		return
	}
	if !ws.pageSupported() {
		return
	}
	t.offset = -1 // force a reload even when the target offset is 0
	ws.gotoOffset(0)
}

// buildCondition renders one column/operator/value triple as SQL. The column
// is quoted for the driver; string values are single-quoted and escaped while
// numeric values stay bare; IN renders each list element as a literal.
func (ws *workspace) buildCondition(col, op, val string) string {
	if col == "" || op == "" {
		return ""
	}
	q := ws.quoteTable(col)
	switch op {
	case "IS NULL", "IS NOT NULL":
		return q + " " + op
	case "IN":
		v := strings.TrimSpace(val)
		if v == "" {
			return ""
		}
		list := inListLiteral(ws.conn.Driver, v)
		if list == "" {
			return ""
		}
		return q + " IN " + list
	}
	v := strings.TrimSpace(val)
	if v == "" {
		return ""
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil && !strings.Contains(strings.ToUpper(op), "LIKE") {
		return q + " " + op + " " + v
	}
	// Route through the kernel's dialect-aware literal quoting: MySQL/MariaDB
	// run in backslash-escape mode, so a literal backslash must be doubled or
	// it escapes the closing quote and the filter runs into the rest of the
	// statement. The hand-rolled doubling below only handled quotes.
	return q + " " + op + " " + sqltext.QuotedLiteral(ws.conn.Driver, v)
}

// inListLiteral renders a comma-separated IN list as "(a, b, c)", quoting each
// element as a literal (numbers stay bare). An outer pair of parentheses is
// accepted as a convenience, but the elements inside are still quoted
// individually — the old branch pasted the raw input in after merely wrapping
// it, which let a value such as `1) OR (1=1` splice a new predicate into the
// WHERE clause. A raw subquery is deliberately not passed through; the raw
// WHERE box is the documented escape hatch for advanced SQL.
func inListLiteral(driver, val string) string {
	inner := strings.TrimSpace(val)
	if strings.HasPrefix(inner, "(") && strings.HasSuffix(inner, ")") && len(inner) >= 2 {
		inner = strings.TrimSpace(inner[1 : len(inner)-1])
	}
	if inner == "" {
		return ""
	}
	parts := strings.Split(inner, ",")
	rendered := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := strconv.ParseFloat(p, 64); err == nil {
			rendered = append(rendered, p)
			continue
		}
		rendered = append(rendered, sqltext.QuotedLiteral(driver, p))
	}
	if len(rendered) == 0 {
		return ""
	}
	return "(" + strings.Join(rendered, ",") + ")"
}
