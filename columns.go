package main

// TablePlus-style Columns sheet: check which columns are visible in the data
// grid. The selection is stored by column name so it survives paging and
// re-queries, and is re-applied whenever a fresh grid is built.

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// applyHidden reconciles a freshly built data-view grid with the hidden-column
// selection, matched by column name.
func (ws *workspace) applyHidden(g *dataTable) {
	if g == nil || g.data == nil || len(ws.hiddenCols) == 0 {
		return
	}
	m := map[int]bool{}
	for i, c := range g.data.Cols {
		if ws.hiddenCols[c.Name] {
			m[i] = true
		}
	}
	if len(m) > 0 {
		g.setHidden(m)
	}
}

// showColumnsDialog opens the column visibility sheet for the open table.
func (ws *workspace) showColumnsDialog() {
	if ws.tableName == "" {
		showInfo(ws.window, "Columns", "Open a table first.")
		return
	}
	cols := ws.columnNames()
	if len(cols) == 0 {
		showInfo(ws.window, "Columns", "Column list is not available yet — wait for the table to load.")
		return
	}

	checks := make([]*widget.Check, 0, len(cols))
	box := container.NewVBox()
	for _, name := range cols {
		chk := widget.NewCheck(name, nil)
		chk.SetChecked(!ws.hiddenCols[name])
		checks = append(checks, chk)
		box.Objects = append(box.Objects, chk)
	}
	box.Refresh()

	setAll := func(v bool) {
		for _, c := range checks {
			c.SetChecked(v)
		}
	}

	var dlg dialog.Dialog
	allBtn := widget.NewButton("All", func() { setAll(true) })
	noneBtn := widget.NewButton("None", func() { setAll(false) })
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })
	applyBtn := widget.NewButton("Apply", func() {
		hidden := map[string]bool{}
		for i, c := range checks {
			if !c.Checked {
				hidden[cols[i]] = true
			}
		}
		ws.hiddenCols = hidden
		ws.applyHidden(ws.grid)
		dlg.Hide()
	})
	applyBtn.Importance = widget.HighImportance

	body := container.NewBorder(
		nil,
		container.NewHBox(allBtn, noneBtn, layout.NewSpacer(), cancelBtn, applyBtn),
		nil, nil,
		container.NewScroll(box),
	)

	dlg = dialog.NewCustom("Columns", "", body, ws.window)
	dlg.Resize(fyne.NewSize(360, 460))
	dlg.Show()
}
