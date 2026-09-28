package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// filterOpLabel pairs the bar's Chinese labels with the engine's operators.
var filterOpLabel = []struct{ label, op string }{
	{"等于", db.FilterEq},
	{"不等于", db.FilterNe},
	{"大于", db.FilterGT},
	{"大于等于", db.FilterGTE},
	{"小于", db.FilterLT},
	{"小于等于", db.FilterLTE},
	{"包含", db.FilterContains},
	{"为 NULL", db.FilterIsNull},
	{"不为 NULL", db.FilterNotNull},
}

// filterCondRow is one condition line of the filter dialog.
type filterCondRow struct {
	column *widget.Select
	op     *widget.Select
	value  *widget.Entry
	line   *fyne.Container
}

func (r *filterCondRow) opCode() string {
	for _, l := range filterOpLabel {
		if l.label == r.op.Selected {
			return l.op
		}
	}
	return db.FilterEq
}

// showFilterDialog builds WHERE conditions column-by-column and re-queries the
// current table. The composed statement goes into the editor first, so what
// runs is exactly what the user can see and edit.
func (c *Client) showFilterDialog() {
	if !c.gridEditable() {
		return
	}
	base, err := c.editableTableName()
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}

	conds := []*filterCondRow{}
	rows := container.NewVBox()
	addCond := func() {
		r := &filterCondRow{}
		r.column = widget.NewSelect(c.grid.cols, nil)
		if len(c.grid.cols) > 0 {
			r.column.SetSelected(c.grid.cols[0])
		}
		labels := make([]string, len(filterOpLabel))
		for i, l := range filterOpLabel {
			labels[i] = l.label
		}
		r.op = widget.NewSelect(labels, nil)
		r.op.SetSelected("等于")
		r.value = widget.NewEntry()
		r.value.SetPlaceHolder("比较值（空值按 NULL 处理）")
		r.op.OnChanged = func(label string) {
			if label == "为 NULL" || label == "不为 NULL" {
				r.value.Disable()
			} else {
				r.value.Enable()
			}
		}
		r.line = container.NewHBox(r.column, r.op, r.value)
		conds = append(conds, r)
		rows.Add(r.line)
		rows.Refresh()
	}
	addCond()

	var d *dialog.CustomDialog
	apply := func() {
		var filters []db.TableFilter
		for _, r := range conds {
			if r.column.Selected == "" {
				continue
			}
			filters = append(filters, db.TableFilter{
				Column: r.column.Selected,
				Op:     r.opCode(),
				Value:  r.value.Text,
			})
		}
		where, err := c.store.TableWhere(c.currentID, c.grid.src.Database, c.grid.src.Schema, c.grid.src.Table, filters)
		if err != nil {
			dialog.ShowError(err, c.win)
			return
		}
		c.tabs.entry.setTextSilently("SELECT * FROM " + base + where)
		d.Hide()
		c.runCurrent()
	}
	clear := func() {
		c.tabs.entry.setTextSilently("SELECT * FROM " + base)
		d.Hide()
		c.runCurrent()
	}

	toolRow := container.NewHBox(
		widget.NewButtonWithIcon("添加条件", theme.ContentAddIcon(), addCond),
		widget.NewButtonWithIcon("移除条件", theme.ContentRemoveIcon(), func() {
			if n := len(conds); n > 0 {
				last := conds[n-1]
				conds = conds[:n-1]
				rows.Remove(last.line)
				rows.Refresh()
			}
		}),
	)
	buttons := container.NewHBox(
		widget.NewButtonWithIcon("应用过滤", theme.ConfirmIcon(), apply),
		widget.NewButtonWithIcon("清除过滤", theme.CancelIcon(), clear),
		layout.NewSpacer(),
	)

	content := container.NewBorder(
		container.NewVBox(toolRow, widget.NewLabel("多条件之间是 AND 关系；应用后语句会填入编辑器，可直接修改再执行。")),
		buttons, nil, nil,
		container.NewVScroll(rows),
	)
	d = dialog.NewCustom("过滤 "+c.grid.src.Table, "关闭", content, c.win)
	d.Resize(fyne.NewSize(560, 340))
	d.Show()
}
