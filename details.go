package main

// Right details sidebar ("详情/助理") and the index page, matching the
// TablePlus workspace layout. The 详情 tab shows the selected row's field
// values; click a value to edit it or the chevron for quick values
// (NULL / EMPTY / NOW()).

import (
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// ---------------------------------------------------------------------------
// Row details (TablePlus right sidebar): one field per selected row, click a
// field to edit it, the chevron opens a menu of quick values.

// detailsField is one column's value in the selected row. In table-structure
// mode (no row selected) it carries a read-only description instead.
type detailsField struct {
	Name     string
	Type     string
	Value    *string
	Col      int
	ReadOnly bool
}

// staticBox is the read-only twin of valueButton (no tap, no chevron).
func staticBox(v *string) fyne.CanvasObject {
	bg := canvas.NewRectangle(pal().crumbBG)
	bg.CornerRadius = 6
	border := canvas.NewRectangle(color.Transparent)
	border.CornerRadius = 6
	border.StrokeColor = pal().crumbBorder
	border.StrokeWidth = 1
	text := "NULL"
	if v != nil {
		text = singleLineText(*v)
	}
	lbl := widget.NewLabel(text)
	lbl.Truncation = fyne.TextTruncateEllipsis
	return container.NewStack(bg, border, container.NewPadded(lbl))
}

// valueButton is the rounded value box under a field name. Tapping the text
// opens the full editor; tapping the chevron on the right opens the
// quick-value menu.
type valueButton struct {
	widget.BaseWidget
	value  *string
	onEdit func()
	onMenu func(pos fyne.Position)
	label  *widget.Label
}

func newValueButton(v *string) *valueButton {
	vb := &valueButton{value: v}
	vb.ExtendBaseWidget(vb)
	return vb
}

func (vb *valueButton) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(pal().crumbBG)
	bg.CornerRadius = 6
	border := canvas.NewRectangle(color.Transparent)
	border.CornerRadius = 6
	border.StrokeColor = pal().crumbBorder
	border.StrokeWidth = 1

	vb.label = widget.NewLabel("")
	vb.label.Truncation = fyne.TextTruncateEllipsis
	if vb.value == nil {
		vb.label.SetText("NULL")
		vb.label.TextStyle.Italic = true
	} else {
		vb.label.SetText(singleLineText(*vb.value))
	}
	chevron := widget.NewIcon(theme.MenuDropDownIcon())
	row := container.NewBorder(nil, nil, nil, chevron, container.NewPadded(vb.label))
	return widget.NewSimpleRenderer(container.NewStack(bg, border, row))
}

func (vb *valueButton) Tapped(e *fyne.PointEvent) {
	// The rightmost ~24px is the chevron hit area.
	if vb.Size().Width > 0 && e.Position.X >= vb.Size().Width-24 {
		if vb.onMenu != nil {
			vb.onMenu(e.AbsolutePosition)
		}
		return
	}
	if vb.onEdit != nil {
		vb.onEdit()
	}
}

func (vb *valueButton) Cursor() desktop.Cursor { return desktop.TextCursor }

func hintLabel(s string) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	l.Wrapping = fyne.TextWrapWord
	return l
}

// isDateTimeType reports whether a column should offer the NOW() quick value.
func isDateTimeType(t string) bool {
	t = strings.ToLower(t)
	return strings.Contains(t, "date") || strings.Contains(t, "time")
}

// ---------------------------------------------------------------------------
// detailsPane: the right sidebar with 详情/助理 tabs, field search box and
// the property list for the current table.

type detailsPane struct {
	widget.BaseWidget
	mode     int // 0 = 详情, 1 = 助理
	search   *widget.Entry
	slot     *fyne.Container
	modeBtns []*sideModeButton

	fields []detailsField
	onEdit func(col int)            // open the full cell editor
	onSet  func(col int, v *string) // a quick value was chosen
}

func newDetailsPane() *detailsPane {
	d := &detailsPane{}
	d.ExtendBaseWidget(d)
	return d
}

// setRow shows the selected row's field values (col/type/value).
func (d *detailsPane) setRow(fields []detailsField) {
	d.fields = fields
	d.rebuild()
}

// setRowPrompt is shown until a row is selected.
func (d *detailsPane) setRowPrompt() {
	d.fields = nil
	d.rebuild()
}

// setTable shows the opened table's column structure (used when no row is
// selected), so the right panel is never blank while a table is open.
func (d *detailsPane) setTable(name string, cols []ColumnInfo) {
	fields := make([]detailsField, 0, len(cols))
	for i, c := range cols {
		desc := c.Type
		if c.Key != "" {
			desc += " · " + c.Key
		}
		if !c.Nullable {
			desc += " · NOT NULL"
		}
		v := desc
		fields = append(fields, detailsField{
			Name: c.Name, Type: c.Type, Value: &v, Col: i, ReadOnly: true,
		})
	}
	d.fields = fields
	d.rebuild()
}

// rebuild renders the field cards, honoring the field search box.
func (d *detailsPane) rebuild() {
	if d.slot == nil {
		return
	}
	q := ""
	if d.search != nil {
		q = strings.ToLower(strings.TrimSpace(d.search.Text))
	}
	if len(d.fields) == 0 {
		empty := widget.NewLabelWithStyle("打开一个表以查看结构，\n或选择一行查看字段值", fyne.TextAlignCenter, fyne.TextStyle{})
		empty.Importance = widget.LowImportance
		d.slot.Objects = []fyne.CanvasObject{container.NewCenter(empty)}
		d.slot.Refresh()
		return
	}
	box := container.NewVBox()
	shown := 0
	for _, f := range d.fields {
		if q != "" && !strings.Contains(strings.ToLower(f.Name), q) {
			continue
		}
		box.Objects = append(box.Objects, d.fieldCard(f))
		shown++
	}
	if shown == 0 {
		box.Objects = append(box.Objects, hintLabel("没有匹配的字段"))
	}
	d.slot.Objects = []fyne.CanvasObject{container.NewScroll(container.NewPadded(box))}
	d.slot.Refresh()
}

// fieldCard renders one field: name + type header over a value box. In table
// mode the box is read-only; for a selected row it is editable.
func (d *detailsPane) fieldCard(f detailsField) fyne.CanvasObject {
	name := canvas.NewText(f.Name, pal().metaKeyFG)
	name.TextSize = 12
	typ := canvas.NewText(f.Type, pal().metaKeyFG)
	typ.TextSize = 11
	head := container.NewBorder(nil, nil, name, typ, layout.NewSpacer())

	if f.ReadOnly {
		return container.NewVBox(head, staticBox(f.Value))
	}

	col := f.Col
	vb := newValueButton(f.Value)
	vb.onEdit = func() {
		if d.onEdit != nil {
			d.onEdit(col)
		}
	}
	vb.onMenu = func(pos fyne.Position) { d.showQuickValues(col, pos) }
	return container.NewVBox(head, vb)
}

// showQuickValues opens the per-field menu (NULL / EMPTY / NOW()).
func (d *detailsPane) showQuickValues(col int, pos fyne.Position) {
	if col < 0 || col >= len(d.fields) {
		return
	}
	canvas := fyne.CurrentApp().Driver().CanvasForObject(d)
	if canvas == nil {
		return
	}
	items := []*fyne.MenuItem{
		fyne.NewMenuItem("NULL", func() { d.applyValue(col, nil) }),
		fyne.NewMenuItem("EMPTY", func() { s := ""; d.applyValue(col, &s) }),
		fyne.NewMenuItem("DEFAULT", func() {
			s := db.DefaultMarker // sentinel: Commit writes the DEFAULT keyword
			d.applyValue(col, &s)
		}),
	}
	if isDateTimeType(d.fields[col].Type) {
		items = append(items, fyne.NewMenuItem("NOW()", func() {
			s := time.Now().Format("2006-01-02 15:04:05")
			d.applyValue(col, &s)
		}))
	}
	widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), canvas, pos)
}

func (d *detailsPane) applyValue(col int, v *string) {
	if d.onSet != nil {
		d.onSet(col, v)
	}
}

func (d *detailsPane) CreateRenderer() fyne.WidgetRenderer {
	detailBtn := newSideModeButton("详情", func() { d.setMode(0) })
	assistBtn := newSideModeButton("助理", func() { d.setMode(1) })
	d.modeBtns = []*sideModeButton{detailBtn, assistBtn}
	for i, b := range d.modeBtns {
		b.setActive(i == d.mode)
	}
	modeRow := container.NewCenter(container.NewHBox(detailBtn, spacerW(4), assistBtn))

	d.search = widget.NewEntry()
	d.search.PlaceHolder = "搜索字段..."
	d.search.OnChanged = func(string) { d.rebuild() }
	clearBtn := newToolButton(theme.CancelIcon(), func() { d.search.SetText("") })
	clearBtn.small = true
	searchRow := container.NewBorder(nil, nil,
		widget.NewIcon(theme.SearchIcon()), clearBtn, d.search)

	assist := widget.NewLabelWithStyle("（暂无助理）", fyne.TextAlignCenter, fyne.TextStyle{Italic: true})

	d.slot = container.NewStack()
	d.rebuild()
	body := container.NewStack(d.slot, assist)

	root := container.NewBorder(
		container.NewVBox(container.NewPadded(modeRow), container.NewPadded(searchRow)),
		nil, nil, nil,
		body,
	)
	d.syncMode(body, assist)
	return widget.NewSimpleRenderer(container.NewStack(
		newPanelBackground(root, func() color.Color { return pal().detailBG }),
	))
}

func (d *detailsPane) setMode(m int) {
	d.mode = m
	for i, b := range d.modeBtns {
		b.setActive(i == m)
	}
	d.Refresh()
}

func (d *detailsPane) syncMode(body *fyne.Container, assist fyne.CanvasObject) {
	if d.mode == 1 {
		assist.Show()
		for _, o := range d.slot.Objects {
			o.Hide()
		}
	} else {
		assist.Hide()
		for _, o := range d.slot.Objects {
			o.Show()
		}
	}
	body.Refresh()
}

func (d *detailsPane) Refresh() {
	if d.slot != nil {
		// re-run visibility sync via the stored renderer objects
		for _, o := range d.slot.Objects {
			if d.mode == 1 {
				o.Hide()
			} else {
				o.Show()
			}
		}
	}
	d.BaseWidget.Refresh()
}

// ---------------------------------------------------------------------------
// listPane: generic header + rows list (used by the 索引 page).

type listPane struct {
	widget.BaseWidget
	headers []string
	rows    [][]string
}

func newListPane(headers []string, rows [][]string) *listPane {
	p := &listPane{headers: headers, rows: rows}
	p.ExtendBaseWidget(p)
	return p
}

func (p *listPane) CreateRenderer() fyne.WidgetRenderer {
	n := len(p.headers)
	header := container.NewGridWithColumns(n)
	for _, h := range p.headers {
		header.Objects = append(header.Objects, boldLabel(h))
	}
	rowsBox := container.NewVBox()
	for _, r := range p.rows {
		row := container.NewGridWithColumns(n)
		for _, c := range r {
			row.Objects = append(row.Objects, plainLabel(c))
		}
		rowsBox.Objects = append(rowsBox.Objects, row)
	}
	if len(p.rows) == 0 {
		empty := canvas.NewText("—", pal().metaKeyFG)
		empty.TextSize = 12
		rowsBox.Objects = append(rowsBox.Objects, empty)
	}
	list := container.NewVBox(header, widget.NewSeparator(), rowsBox)
	return widget.NewSimpleRenderer(container.NewScroll(container.NewPadded(list)))
}

// ---------------------------------------------------------------------------
// information_schema helpers (MySQL; other drivers fail gracefully).

var sqlQuote = strings.NewReplacer("'", "''")

// fetchIndexes reads index metadata for the 索引 page.
func fetchIndexes(sess db.Session, database, table string) *TableData {
	if sess == nil || database == "" {
		return nil
	}
	sql := "SELECT INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, " +
		"CASE WHEN NON_UNIQUE=0 THEN 'YES' ELSE 'NO' END, INDEX_TYPE " +
		"FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='" + sqlQuote.Replace(database) +
		"' AND TABLE_NAME='" + sqlQuote.Replace(table) + "' ORDER BY INDEX_NAME, SEQ_IN_INDEX"
	res, err := sess.Query(sql, 2000)
	if err != nil {
		return nil
	}
	data := &TableData{Fetched: len(res.Rows)}
	for _, c := range res.Columns {
		data.Cols = append(data.Cols, ColumnInfo{Name: c.Name})
	}
	data.Rows = res.Rows
	return data
}
