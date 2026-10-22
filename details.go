package main

// Right details sidebar ("详情/助理") and the index page, matching the
// TablePlus workspace layout. The 详情 tab shows the selected row's field
// values; click a value to edit it or the chevron for quick values
// (NULL / EMPTY / NOW()).

import (
	"bytes"
	"encoding/json"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
	"fynequery/icons"
)

// prettyJSON re-indents a JSON object/array value so it reads as a structured
// block instead of one long line. It reports false for non-JSON text.
func prettyJSON(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") {
		return s, false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(t), "", "  "); err != nil {
		return s, false
	}
	return buf.String(), true
}

// capLines trims a multi-line value to maxLines so a huge JSON blob cannot
// stretch one field card across the whole panel; the ellipsis marks the cut.
func capLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + "\n…"
}

// detailLabel builds the value label: JSON becomes an indented monospace block
// (capped), everything else stays a single truncated line.
func detailLabel(v *string) *widget.Label {
	lbl := widget.NewLabel("NULL")
	if v == nil {
		lbl.TextStyle.Italic = true
		return lbl
	}
	if pretty, ok := prettyJSON(*v); ok {
		lbl.SetText(capLines(pretty, 14))
		lbl.TextStyle.Monospace = true
		lbl.Wrapping = fyne.TextWrapWord
		lbl.Truncation = fyne.TextTruncateClip
		return lbl
	}
	lbl.SetText(singleLineText(*v))
	lbl.Truncation = fyne.TextTruncateEllipsis
	return lbl
}

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
	return container.NewStack(bg, border, container.NewPadded(detailLabel(v)))
}

// valueButton is the rounded value box under a field name. Tapping the text
// opens the full editor; tapping the chevron on the right opens the
// quick-value menu.
type valueButton struct {
	widget.BaseWidget
	value  *string
	onEdit func()
	onMenu func(pos fyne.Position)
	onView func() // double-click: open the full-value viewer
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

	vb.label = detailLabel(vb.value)
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

// DoubleTapped opens the full-value viewer (the "查看全部" affordance for
// JSON / long text that the inline box truncates).
func (vb *valueButton) DoubleTapped(*fyne.PointEvent) {
	if vb.onView != nil {
		vb.onView()
	}
}

func hintLabel(s string) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	l.Wrapping = fyne.TextWrapWord
	return l
}

// showFullValueDialog opens the "查看全部" viewer: the complete (pretty-printed
// when JSON) value in a scrollable monospace box with a copy button. The inline
// details box truncates long values, so this is the only place the whole blob
// is readable.
func showFullValueDialog(w fyne.Window, title, raw string) {
	text := raw
	if pretty, ok := prettyJSON(raw); ok {
		text = pretty
	}
	entry := widget.NewMultiLineEntry()
	entry.SetText(text)
	entry.TextStyle.Monospace = true
	entry.Wrapping = fyne.TextWrapOff // long JSON lines scroll horizontally

	var dlg dialog.Dialog
	copyBtn := widget.NewButton("复制", func() {
		fyne.CurrentApp().Clipboard().SetContent(raw)
	})
	closeBtn := widget.NewButton("关闭", func() { dlg.Hide() })
	body := container.NewBorder(nil,
		container.NewHBox(layout.NewSpacer(), copyBtn, closeBtn),
		nil, nil, entry)
	dlg = dialog.NewCustom(title, "", body, w)
	dlg.Resize(fyne.NewSize(620, 460))
	dlg.Show()
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
	mode      int // 0 = 详情, 1 = 帮助
	search    *widget.Entry
	searchRow fyne.CanvasObject
	slot      *fyne.Container
	modeBtns  []*sideModeButton
	help      fyne.CanvasObject
	body      *fyne.Container

	fields []detailsField
	onEdit func(col int)            // open the full cell editor
	onSet  func(col int, v *string) // a quick value was chosen
	onView func(f detailsField)     // open the full-value viewer (查看全部)
}

func newDetailsPane() *detailsPane {
	d := &detailsPane{}
	d.ExtendBaseWidget(d)
	// Field cards bake palette colours into canvas.Text/rectangles; rebuild
	// them on a theme switch so the right panel recolours with the rest (a
	// plain Refresh would just repaint the stale baked colours).
	onThemeChange(func() { d.rebuild() })
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
	vb.onView = func() { d.viewField(col) }
	vb.onMenu = func(pos fyne.Position) { d.showQuickValues(col, pos) }
	return container.NewVBox(head, vb)
}

// viewField opens the full-value viewer for one field (no-op out of range).
func (d *detailsPane) viewField(col int) {
	if col < 0 || col >= len(d.fields) || d.onView == nil {
		return
	}
	d.onView(d.fields[col])
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
		fyne.NewMenuItem("查看全部", func() { d.viewField(col) }),
		fyne.NewMenuItemSeparator(),
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
	helpBtn := newSideModeButton("帮助", func() { d.setMode(1) })
	d.modeBtns = []*sideModeButton{detailBtn, helpBtn}
	for i, b := range d.modeBtns {
		b.setActive(i == d.mode)
	}
	modeRow := container.NewCenter(container.NewHBox(detailBtn, spacerW(4), helpBtn))

	d.search = widget.NewEntry()
	d.search.PlaceHolder = "搜索字段..."
	d.search.OnChanged = func(string) { d.rebuild() }
	clearBtn := newToolButton(icons.Cancel, func() { d.search.SetText("") })
	clearBtn.small = true
	d.searchRow = container.NewBorder(nil, nil,
		widget.NewIcon(icons.Search), clearBtn, d.search)

	help := d.buildHelpContent()

	d.slot = container.NewStack()
	d.rebuild()
	d.body = container.NewStack(d.slot, help)
	d.help = help

	root := container.NewBorder(
		container.NewVBox(container.NewPadded(modeRow), container.NewPadded(d.searchRow)),
		nil, nil, nil,
		d.body,
	)
	d.syncMode()
	return widget.NewSimpleRenderer(container.NewStack(
		newPanelBackground(root, func() color.Color { return pal().detailBG }),
	))
}

// buildHelpContent renders the 帮助 tab: keyboard shortcuts and usage tips.
// Everything uses theme-aware widgets (no baked canvas colours), so it recolours
// with the light/dark switch for free.
func (d *detailsPane) buildHelpContent() fyne.CanvasObject {
	box := container.NewVBox()
	addSection := func(title string, rows []string) {
		box.Objects = append(box.Objects, container.NewPadded(helpHeader(title)))
		for _, r := range rows {
			box.Objects = append(box.Objects, container.NewPadded(helpRow(r)))
		}
		box.Objects = append(box.Objects, spacerH(12))
	}
	addSection("快捷键", []string{
		"Ctrl+Enter — 执行当前语句",
		"Ctrl+Space — 自动补全（关键字 / 函数 / 表 / 列）",
		"Ctrl+K — 快速打开（连接 / 数据库 / 表）",
		"Enter / Shift+Enter — 结果内查找：下一个 / 上一个",
	})
	addSection("小提示", []string{
		"右键侧栏项：打开 / 查看结构 / 复制名称·CREATE·SELECT·TRUNCATE·DROP / Clone / Truncate / Delete",
		"右键欢迎页连接：编辑 / 删除",
		"详情栏点值就地编辑；点右侧箭头选 NULL / EMPTY / DEFAULT / NOW()",
		"改动进入底部事务条：Commit 写回 · Rollback 放弃 · Preview 预览 SQL",
		"结果可 Pin 固定，重跑后仍可对比",
		"编辑器上方 Beautify 整理 SQL 排版",
	})
	return container.NewScroll(container.NewPadded(box))
}

func helpHeader(s string) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return l
}

func helpRow(s string) fyne.CanvasObject {
	l := widget.NewLabel(s)
	l.Wrapping = fyne.TextWrapWord
	return l
}

func (d *detailsPane) setMode(m int) {
	d.mode = m
	for i, b := range d.modeBtns {
		b.setActive(i == m)
	}
	d.syncMode()
}

func (d *detailsPane) syncMode() {
	helpMode := d.mode == 1
	if d.help != nil {
		if helpMode {
			d.help.Show()
		} else {
			d.help.Hide()
		}
	}
	if d.searchRow != nil {
		if helpMode {
			d.searchRow.Hide()
		} else {
			d.searchRow.Show()
		}
	}
	if d.slot != nil {
		for _, o := range d.slot.Objects {
			if helpMode {
				o.Hide()
			} else {
				o.Show()
			}
		}
	}
	if d.body != nil {
		d.body.Refresh()
	}
}

func (d *detailsPane) Refresh() {
	d.syncMode()
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
		empty := widget.NewLabel("—")
		empty.Importance = widget.LowImportance
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
