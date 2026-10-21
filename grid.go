package main

// DataTable: the main data grid modeled on TablePlus's table view —
// sticky header, row-number gutter, zebra rows, selected-row highlight,
// right-aligned numbers, dimmed NULL cells.

import (
	"fmt"
	"image/color"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// Find-highlight colours (a result-grid "spotlight", mirroring TablePlus' in
// grid search). A hit cell gets a warm background and bold text.
var (
	findHighlightBG = color.RGBA{R: 0xFF, G: 0xE5, B: 0x7F, A: 0xFF}
	findHighlightFG = color.RGBA{R: 0x33, G: 0x24, B: 0x00, A: 0xFF}
)

const (
	gutterWidth float32 = 52 // row-number gutter
	rowHeight   float32 = 30
	headerH     float32 = 30
	minColWidth float32 = 96
)

type dataTable struct {
	widget.BaseWidget
	data   *TableData
	selRow int
	status *statusBar
	hidden map[int]bool // column indices hidden by the Columns dialog

	// editable-grid state (see edit.go)
	window      fyne.Window
	onDirty     func()
	onRowSelect func(row int) // fired when the selected row changes
	editable    bool
	pkCols      []int
	dirty       map[int]map[int]*string // row -> col -> new value (nil = NULL)
	deleted     map[int]bool            // rows marked for deletion

	// column sort state (TablePlus HeaderSort / HeaderSortDirection)
	sortCol int
	sortAsc bool

	// in-result search state (TablePlus in-grid spotlight)
	findTerm     string
	findHits     []searchHitKey
	findHitSet   map[searchHitKey]bool
	findCur      int
	onFindStatus func(cur, total int) // updates the counter label when hits change

	// copy/Query-Magics context: owning table name ("" for query results) and
	// the workspace hook behind __format__goto column links.
	tableName string
	onGoto    func(table, column, value string)
	onPin     func() // optional: pin this result (query pane)
}

func newDataTable(d *TableData, status *statusBar) *dataTable {
	t := &dataTable{data: d, status: status, selRow: -1, sortCol: -1}
	t.ExtendBaseWidget(t)
	t.Refresh() // populate header/cell text immediately (not just on theme apply)
	return t
}

// colTitle returns the column's display title with any Query-Magics suffix
// (__format__… / __goto__…) stripped.
func (t *dataTable) colTitle(col int) string {
	if col < 0 || col >= len(t.data.Cols) {
		return ""
	}
	base, _ := splitMagic(t.data.Cols[col].Name)
	return base
}

// cellStr returns the raw cell value (nil = NULL) at (row, col), or nil when
// the row/column is out of range.
func cellStr(row []*string, col int) *string {
	if row == nil || col < 0 || col >= len(row) {
		return nil
	}
	return row[col]
}

// searchHitKey packs a cell position into one map key for the find highlight set.
type searchHitKey [2]int

// setHidden applies a hidden-column mask (by index) and re-lays-out the grid.
func (t *dataTable) setHidden(m map[int]bool) {
	t.hidden = m
	t.Refresh()
	t.Resize(t.Size()) // force a re-layout so widths/visibility take effect
}

func (t *dataTable) isHidden(i int) bool {
	return t.hidden != nil && t.hidden[i]
}

func (t *dataTable) visibleCount() int {
	n := 0
	for i := range t.data.Cols {
		if !t.isHidden(i) {
			n++
		}
	}
	return n
}

func (t *dataTable) setData(d *TableData) {
	t.data = d
	t.selRow = -1
	t.sortCol = -1
	t.sortAsc = true
	t.findTerm = ""
	t.findHits = nil
	t.findHitSet = nil
	t.findCur = 0
	t.Refresh()
	if t.status != nil {
		t.status.setRows(d.Fetched, d.Total, d.Approx)
	}
}

// sortData sorts the displayed rows by the tapped column. NULLs always sort
// last regardless of direction; tapping the sorted column again flips
// ascending/descending; numbers compare numerically only when the column's
// declared type is numeric (a text column of digits stays lexicographic).
// Mirrors TablePlus HeaderSort / HeaderSortDirection.
func (t *dataTable) sortData(col int) {
	if col < 0 || col >= len(t.data.Cols) {
		return
	}
	if t.sortCol == col {
		t.sortAsc = !t.sortAsc
	} else {
		t.sortCol = col
		t.sortAsc = true
	}
	asc := t.sortAsc
	numeric := isNumericType(t.data.Cols[col].Type)
	sort.SliceStable(t.data.Rows, func(i, j int) bool {
		if i >= len(t.data.Rows) || j >= len(t.data.Rows) {
			return false
		}
		a, b := cellStr(t.data.Rows[i], col), cellStr(t.data.Rows[j], col)
		aNull, bNull := a == nil, b == nil
		if aNull != bNull {
			return !aNull // non-NULL first; NULLs always sink to the bottom
		}
		if aNull {
			return false
		}
		av, bv := *a, *b
		if numeric {
			if fa, ea := strconv.ParseFloat(av, 64); ea == nil {
				if fb, eb := strconv.ParseFloat(bv, 64); eb == nil {
					if asc {
						return fa < fb
					}
					return fa > fb
				}
			}
		}
		if asc {
			return av < bv
		}
		return av > bv
	})
	t.Refresh()
	t.rescanFind() // a re-order invalidates collected hit positions
}

// setFindTerm re-scans the grid for the term and spotlights the first hit.
func (t *dataTable) setFindTerm(term string) {
	t.findTerm = term
	t.findHits = nil
	t.findHitSet = nil
	t.findCur = 0
	needle := strings.ToLower(term)
	if needle != "" {
		t.scanFindHits(needle)
		t.buildFindSet()
		if len(t.findHits) > 0 {
			t.showFindHit(0)
		}
	}
	t.updateFindStatus()
	t.Refresh()
}

// scanFindHits collects every cell whose displayed text contains the needle,
// in reading order. Comparing cellText means pending edits are searched too.
func (t *dataTable) scanFindHits(needle string) {
	for r := 0; r < len(t.data.Rows); r++ {
		for c := 0; c < len(t.data.Cols); c++ {
			if strings.Contains(strings.ToLower(t.cellText(r, c)), needle) {
				t.findHits = append(t.findHits, searchHitKey{r, c})
			}
		}
	}
}

// buildFindSet indexes the collected hits for the per-cell highlight test.
func (t *dataTable) buildFindSet() {
	t.findHitSet = nil
	if len(t.findHits) == 0 {
		return
	}
	t.findHitSet = make(map[searchHitKey]bool, len(t.findHits))
	for _, h := range t.findHits {
		t.findHitSet[h] = true
	}
}

// cellText returns the effective display text of one cell (pending edits
// applied, nil -> "NULL", DEFAULT sentinel rendered as the keyword).
func (t *dataTable) cellText(row, col int) string {
	v := cellStr(t.data.Rows[row], col)
	if m := t.dirty[row]; m != nil {
		if e, ok := m[col]; ok {
			v = e
		}
	}
	if v == nil {
		return "NULL"
	}
	if *v == db.DefaultMarker {
		return "DEFAULT"
	}
	return *v
}

// rescanFind re-runs the scan after the grid's values or order changed while
// a term is active, so highlights keep pointing at the cells they were
// collected for.
func (t *dataTable) rescanFind() {
	if t.findTerm != "" {
		t.findHits = nil
		t.scanFindHits(strings.ToLower(t.findTerm))
		t.buildFindSet()
		if len(t.findHits) == 0 {
			t.findCur = 0
		} else if t.findCur >= len(t.findHits) {
			t.findCur = len(t.findHits) - 1
		}
	}
	t.updateFindStatus()
	t.Refresh()
}

// stepFind moves the spotlight one hit forward or backward, wrapping.
func (t *dataTable) stepFind(delta int) {
	if len(t.findHits) == 0 {
		return
	}
	t.showFindHit((t.findCur + delta + len(t.findHits)) % len(t.findHits))
}

// showFindHit selects hit k. Selecting the row scrolls it into view (the row
// renderer re-reads the data) and fills the detail pane.
func (t *dataTable) showFindHit(k int) {
	t.findCur = k
	if k >= 0 && k < len(t.findHits) {
		t.selectRow(t.findHits[k][0])
	}
	t.updateFindStatus()
	t.Refresh()
}

// isFindHit reports whether the cell matches the active search.
func (t *dataTable) isFindHit(row, col int) bool {
	if t.findTerm == "" || t.findHitSet == nil {
		return false
	}
	return t.findHitSet[searchHitKey{row, col}]
}

// updateFindStatus pushes the "cur/total" counter to the owning label.
func (t *dataTable) updateFindStatus() {
	if t.onFindStatus != nil {
		// A non-empty term that matches nothing must read 0/0, not 1/0.
		if t.findTerm == "" || len(t.findHits) == 0 {
			t.onFindStatus(0, 0)
		} else {
			t.onFindStatus(t.findCur+1, len(t.findHits))
		}
	}
}

// appendRows streams a batch of rows into the grid (TablePlus
// TableContentV4.AppendData). Used by the query pane to paint large results
// incrementally instead of blocking on one giant rebuild.
func (t *dataTable) appendRows(rows [][]*string) {
	t.data.Rows = append(t.data.Rows, rows...)
	t.data.Fetched = len(t.data.Rows)
	if t.data.Fetched > t.data.Total {
		t.data.Total = t.data.Fetched
	}
	t.Refresh()
	t.rescanFind()
}

// selectRow moves the row highlight and notifies the workspace so the right
// sidebar can show the row's field values.
func (t *dataTable) selectRow(i int) {
	t.selRow = i
	t.Refresh()
	if t.onRowSelect != nil {
		t.onRowSelect(i)
	}
}

// rowValues returns the effective values (pending edits applied) of one row.
func (t *dataTable) rowValues(row int) []*string {
	if row < 0 || row >= len(t.data.Rows) {
		return nil
	}
	out := make([]*string, len(t.data.Cols))
	for i := range t.data.Cols {
		if i < len(t.data.Rows[row]) {
			out[i] = t.data.Rows[row][i]
		}
		if m := t.dirty[row]; m != nil {
			if v, ok := m[i]; ok {
				out[i] = v
			}
		}
	}
	return out
}

func (t *dataTable) columnWidths(avail float32) []float32 {
	n := len(t.data.Cols)
	if n == 0 {
		return nil
	}
	vis := t.visibleCount()
	if vis == 0 {
		vis = 1
	}
	w := (avail - gutterWidth) / float32(vis)
	if w < minColWidth {
		w = minColWidth
	}
	out := make([]float32, n)
	for i := range out {
		if t.isHidden(i) {
			out[i] = 0
			continue
		}
		out[i] = w
	}
	return out
}

// totalMinWidth is the full content width of the grid (gutter + all visible columns).
func (t *dataTable) totalMinWidth() float32 {
	if len(t.data.Cols) == 0 {
		return gutterWidth + 100
	}
	vis := t.visibleCount()
	if vis == 0 {
		vis = 1
	}
	return gutterWidth + float32(vis)*minColWidth
}

func (t *dataTable) CreateRenderer() fyne.WidgetRenderer {
	head := newHeaderWidget(t)
	rows := newRowsWidget(t)
	inner := container.NewBorder(head, nil, nil, nil, rows)
	return widget.NewSimpleRenderer(container.NewHScroll(inner))
}

// ---------------------------------------------------------------------------
// headerWidget: sticky column header strip.

type headerWidget struct {
	widget.BaseWidget
	t         *dataTable
	bg        *canvas.Rectangle
	corner    *canvas.Text
	labels    []*canvas.Text
	seps      []*canvas.Rectangle
	colBounds []colSpan // visible column hit-test spans (local coords)
}

// colSpan records a visible column's horizontal extent and index for header
// tap hit-testing.
type colSpan struct {
	x0, x1 float32
	col    int
}

func newHeaderWidget(t *dataTable) *headerWidget {
	h := &headerWidget{t: t}
	h.ExtendBaseWidget(h)
	return h
}

func (h *headerWidget) CreateRenderer() fyne.WidgetRenderer {
	h.bg = canvas.NewRectangle(pal().gridHeaderBG)
	h.corner = canvas.NewText("#", pal().gridHeaderFG)
	h.corner.TextSize = 12
	h.corner.TextStyle.Bold = true
	objs := []fyne.CanvasObject{h.bg, h.corner}
	for range h.t.data.Cols {
		l := canvas.NewText("", pal().gridHeaderFG)
		l.TextSize = 12
		l.TextStyle.Bold = true
		h.labels = append(h.labels, l)
		sep := canvas.NewRectangle(pal().line)
		h.seps = append(h.seps, sep)
		objs = append(objs, l, sep)
	}
	return &headerRenderer{h: h, objs: objs}
}

type headerRenderer struct {
	h    *headerWidget
	objs []fyne.CanvasObject
}

func (r *headerRenderer) Objects() []fyne.CanvasObject { return r.objs }

func (r *headerRenderer) Layout(size fyne.Size) {
	widths := r.h.t.columnWidths(size.Width)
	r.h.bg.Resize(fyne.NewSize(size.Width, headerH))
	r.h.corner.Move(fyne.NewPos(8, (headerH-r.h.corner.MinSize().Height)/2))
	r.h.colBounds = r.h.colBounds[:0]
	x := gutterWidth + 10
	for i, l := range r.h.labels {
		if i >= len(r.h.t.data.Cols) || i >= len(widths) {
			break
		}
		if r.h.t.isHidden(i) {
			continue
		}
		lw := widths[i]
		lm := l.MinSize()
		if isNumericType(r.h.t.data.Cols[i].Type) {
			l.Move(fyne.NewPos(x+lw-14-lm.Width, (headerH-lm.Height)/2))
		} else {
			l.Move(fyne.NewPos(x, (headerH-lm.Height)/2))
		}
		r.h.seps[i].Resize(fyne.NewSize(1, headerH-10))
		r.h.seps[i].Move(fyne.NewPos(x+lw-1, 5))
		r.h.colBounds = append(r.h.colBounds, colSpan{x0: x, x1: x + lw, col: i})
		x += lw
	}
}

// Tapped sorts the grid by the column under the pointer (TablePlus lets you
// click a header to reorder). The position is in the header's local space.
func (h *headerWidget) Tapped(e *fyne.PointEvent) {
	if e.Position.Y > headerH {
		return
	}
	for _, c := range h.colBounds {
		if e.Position.X >= c.x0 && e.Position.X <= c.x1 {
			h.t.sortData(c.col)
			return
		}
	}
}

func (r *headerRenderer) MinSize() fyne.Size {
	return fyne.NewSize(r.h.t.totalMinWidth(), headerH)
}

func (r *headerRenderer) Refresh() {
	p := pal()
	r.h.bg.FillColor = p.gridHeaderBG
	r.h.bg.Refresh()
	r.h.corner.Color = p.gridHeaderFG
	r.h.corner.Refresh()
	for i, l := range r.h.labels {
		if i >= len(r.h.t.data.Cols) {
			break
		}
		if r.h.t.isHidden(i) {
			l.Hide()
			r.h.seps[i].Hide()
			continue
		}
		l.Show()
		r.h.seps[i].Show()
		l.Text = r.h.t.colTitle(i)
		if r.h.t.sortCol == i {
			if r.h.t.sortAsc {
				l.Text += " ▲"
			} else {
				l.Text += " ▼"
			}
		}
		// TablePlus highlights primary-key column headers in gold.
		if r.h.t.data.Cols[i].Key == "PK" {
			l.Color = p.pkHeader
		} else {
			l.Color = p.gridHeaderFG
		}
		l.Refresh()
		r.h.seps[i].FillColor = p.gridLine
		r.h.seps[i].Refresh()
	}
}

func (r *headerRenderer) Destroy() {}

// ---------------------------------------------------------------------------
// rowsWidget: a vertically scrollable stack of pre-built rows.

type rowsWidget struct {
	widget.BaseWidget
	t     *dataTable
	inner *fyne.Container // VBox of all rows
}

func newRowsWidget(t *dataTable) *rowsWidget {
	rw := &rowsWidget{t: t}
	rw.ExtendBaseWidget(rw)
	return rw
}

func (rw *rowsWidget) CreateRenderer() fyne.WidgetRenderer {
	box := container.NewVBox()
	for i := range rw.t.data.Rows {
		box.Objects = append(box.Objects, newRowView(rw.t, i))
	}
	rw.inner = box
	return widget.NewSimpleRenderer(container.NewVScroll(box))
}

func (rw *rowsWidget) MinSize() fyne.Size {
	return fyne.NewSize(rw.t.totalMinWidth(), rowHeight*4)
}

// ---------------------------------------------------------------------------
// rowView draws one row: gutter + N cells.
type rowView struct {
	widget.BaseWidget
	t       *dataTable
	idx     int
	cells   []*cellView
	gut     *canvas.Text
	gutBG   *canvas.Rectangle
	topLine *canvas.Rectangle
}

func newRowView(t *dataTable, i int) *rowView {
	rv := &rowView{t: t, idx: i}
	n := len(t.data.Cols)
	rv.cells = make([]*cellView, n)
	for ci := 0; ci < n; ci++ {
		rv.cells[ci] = newCellView(t, ci, i)
	}
	rv.ExtendBaseWidget(rv)
	return rv
}

func (rv *rowView) Tapped(*fyne.PointEvent) {
	rv.t.selectRow(rv.idx)
}

// TappedSecondary pops the copy / IN-clause context menu for the row.
func (rv *rowView) TappedSecondary(e *fyne.PointEvent) {
	if rv.t.selRow != rv.idx {
		rv.t.selectRow(rv.idx)
	}
	rv.t.copyMenu(rv.idx, rv.colAtX(e.Position.X), e.AbsolutePosition)
}

// colAtX resolves the visible column under a row-local x coordinate, or -1
// when the pointer is on the row-number gutter.
func (rv *rowView) colAtX(x float32) int {
	if x < gutterWidth {
		return -1
	}
	x -= gutterWidth
	for ci, w := range rv.t.columnWidths(rv.Size().Width) {
		if rv.t.isHidden(ci) || w == 0 {
			continue
		}
		if x < w {
			return ci
		}
		x -= w
	}
	return -1
}

func (rv *rowView) Cursor() desktop.Cursor { return desktop.DefaultCursor }

func (rv *rowView) CreateRenderer() fyne.WidgetRenderer {
	rv.gut = canvas.NewText(fmt.Sprintf("%d", rv.idx+1), pal().gridNullFG)
	rv.gut.TextSize = 12
	rv.gut.Alignment = fyne.TextAlignTrailing
	rv.gutBG = canvas.NewRectangle(pal().gutterBG)
	rv.topLine = canvas.NewRectangle(pal().gridLine)
	objs := []fyne.CanvasObject{rv.gutBG, rv.topLine, rv.gut}
	for _, c := range rv.cells {
		objs = append(objs, c)
	}
	return &rowViewRenderer{rv: rv, objs: objs}
}

type rowViewRenderer struct {
	rv   *rowView
	objs []fyne.CanvasObject
}

func (r *rowViewRenderer) Objects() []fyne.CanvasObject { return r.objs }

func (r *rowViewRenderer) MinSize() fyne.Size {
	return fyne.NewSize(r.rv.t.totalMinWidth(), rowHeight)
}

func (r *rowViewRenderer) Layout(size fyne.Size) {
	widths := r.rv.t.columnWidths(size.Width)
	r.rv.gutBG.Resize(fyne.NewSize(gutterWidth, size.Height))
	r.rv.topLine.Resize(fyne.NewSize(size.Width, 1))
	r.rv.gut.Resize(fyne.NewSize(gutterWidth-10, size.Height))
	r.rv.gut.Move(fyne.NewPos(4, (size.Height-r.rv.gut.MinSize().Height)/2))
	x := gutterWidth
	for ci, cv := range r.rv.cells {
		if ci >= len(widths) {
			break
		}
		if r.rv.t.isHidden(ci) {
			continue
		}
		cv.Resize(fyne.NewSize(widths[ci], size.Height))
		cv.Move(fyne.NewPos(x, 0))
		x += widths[ci]
	}
}

func (r *rowViewRenderer) Refresh() {
	r.rv.gut.Text = fmt.Sprintf("%d", r.rv.idx+1)
	p := pal()
	if r.rv.idx == r.rv.t.selRow {
		r.rv.gut.Color = p.gridHeaderFG
	} else {
		r.rv.gut.Color = p.gridNullFG
	}
	r.rv.gutBG.FillColor = p.gutterBG
	if r.rv.idx == r.rv.t.selRow {
		r.rv.gutBG.FillColor = p.gridRowSelectBG
	}
	r.rv.topLine.FillColor = p.gridLine
	r.rv.gut.Refresh()
	r.rv.gutBG.Refresh()
	r.rv.topLine.Refresh()
	for _, cv := range r.rv.cells {
		if r.rv.t.isHidden(cv.col) {
			cv.Hide()
			continue
		}
		cv.Show()
		cv.refreshCell()
	}
}

func (r *rowViewRenderer) Destroy() {}

// cellView is one cell: background + text.
type cellView struct {
	widget.BaseWidget
	t   *dataTable
	col int
	row int
}

func newCellView(t *dataTable, col, row int) *cellView {
	cv := &cellView{t: t, col: col, row: row}
	cv.ExtendBaseWidget(cv)
	return cv
}

func (cv *cellView) refreshCell() { cv.Refresh() }

func (cv *cellView) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.Transparent)
	text := canvas.NewText("", pal().gridCellFG)
	text.TextSize = 12
	return &cellRenderer{cv: cv, bg: bg, text: text}
}

type cellRenderer struct {
	cv   *cellView
	bg   *canvas.Rectangle
	text *canvas.Text
}

func (r *cellRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.text}
}

func (r *cellRenderer) Layout(size fyne.Size) {
	r.bg.Resize(fyne.NewSize(size.Width-1, size.Height))
	r.text.Resize(fyne.NewSize(size.Width-24, size.Height))
}

func (r *cellRenderer) MinSize() fyne.Size { return fyne.NewSize(60, rowHeight) }

func (r *cellRenderer) Refresh() {
	cv := r.cv
	p := pal()
	var cell *string
	if cv.row < len(cv.t.data.Rows) && cv.col < len(cv.t.data.Rows[cv.row]) {
		cell = cv.t.data.Rows[cv.row][cv.col]
	}
	edited := false
	if m := cv.t.dirty[cv.row]; m != nil {
		if v, ok := m[cv.col]; ok {
			cell = v
			edited = true
		}
	}
	isNull := cell == nil
	numeric := cv.col < len(cv.t.data.Cols) && isNumericType(cv.t.data.Cols[cv.col].Type)

	if isNull {
		r.text.Text = "NULL"
		r.text.Color = p.gridNullFG
		r.text.TextStyle = fyne.TextStyle{Italic: true}
	} else {
		// TablePlus color-codes cell values by column type; Query Magics
		// (__format__ aliases) reshape the text/colour first.
		raw := *cell
		r.text.Text = raw
		r.text.Color = cellColor(cv, p)
		r.text.TextStyle = fyne.TextStyle{}
		if cv.col < len(cv.t.data.Cols) {
			_, magic := splitMagic(cv.t.data.Cols[cv.col].Name)
			if strings.HasPrefix(magic, "goto:") {
				// a table-jump link (TablePlus foreign-key arrow)
				r.text.Color = p.statusLink
				r.text.TextStyle = fyne.TextStyle{Italic: true}
			} else if magic != "" {
				if txt, c, style, ok := renderMagic(magic, raw, p); ok {
					r.text.Text = txt
					r.text.Color = c
					r.text.TextStyle = style
				}
			}
		}
	}
	if edited {
		r.text.Color = p.cellEdited
	}
	if numeric && !isNull {
		r.text.Alignment = fyne.TextAlignTrailing
	} else {
		r.text.Alignment = fyne.TextAlignLeading
	}
	if cv.t.isFindHit(cv.row, cv.col) {
		r.text.Color = findHighlightFG
		r.text.TextStyle = fyne.TextStyle{Bold: true}
	}
	switch {
	case cv.t.deleted[cv.row]:
		r.bg.FillColor = p.rowDeletedBG
	case cv.row == cv.t.selRow:
		r.bg.FillColor = p.gridRowSelectBG
	case cv.t.isFindHit(cv.row, cv.col):
		r.bg.FillColor = findHighlightBG
	case cv.row%2 == 1:
		r.bg.FillColor = p.gridRowAltBG
	default:
		r.bg.FillColor = color.Transparent
	}
	r.bg.Refresh()
	r.text.Refresh()
}

// Tapped follows Query-Magics links (link / email / goto); plain taps fall
// through to the row selection.
func (cv *cellView) Tapped(e *fyne.PointEvent) {
	if cv.col < len(cv.t.data.Cols) && cv.row < len(cv.t.data.Rows) {
		base, magic := splitMagic(cv.t.data.Cols[cv.col].Name)
		v := cellStr(cv.t.data.Rows[cv.row], cv.col)
		if magic != "" && v != nil && magicTap(magic, base, *v, cv.t) {
			return
		}
	}
	cv.t.selectRow(cv.row)
}

// DoubleTapped opens the cell editor when the grid is in edit mode.
func (cv *cellView) DoubleTapped(*fyne.PointEvent) {
	if cv.t.editable {
		cv.t.beginEdit(cv.row, cv.col)
	}
}

// cellColor picks the value color for a cell from its column's SQL type,
// mirroring TablePlus' color-coded data grid.
func cellColor(cv *cellView, p *palette) color.Color {
	if cv.col >= len(cv.t.data.Cols) {
		return p.cellDefault
	}
	switch strings.ToLower(cv.t.data.Cols[cv.col].Type) {
	case "int", "int2", "int4", "int8", "integer", "tinyint", "smallint",
		"mediumint", "bigint", "serial", "bigserial", "smallserial", "bit", "bool", "boolean":
		return p.cellInt
	case "numeric", "decimal", "float", "float4", "float8", "double",
		"double precision", "real", "money", "number":
		return p.cellFloat
	case "date", "datetime", "timestamp", "time", "year", "timestamptz":
		return p.cellDate
	case "char", "varchar", "text", "string", "enum", "uuid", "json",
		"jsonb", "nvarchar", "nchar", "longtext", "mediumtext":
		return p.cellStr
	}
	return p.cellDefault
}

func (r *cellRenderer) Destroy() {}

func isNumericType(t string) bool {
	switch t {
	case "int", "integer", "tinyint", "smallint", "mediumint", "bigint",
		"int2", "int4", "int8", "serial", "bigserial", "smallserial",
		"float", "float4", "float8", "double", "double precision", "real",
		"numeric", "decimal", "money", "number", "year":
		return true
	}
	return false
}

// gridSearchEntry is the in-result search box. Plain Enter advances to the
// next hit, Shift+Enter goes back; both are swallowed so no newline is
// inserted. Fyne does not deliver modifiers through TypedKey, so shift state
// is tracked from KeyDown/KeyUp.
type gridSearchEntry struct {
	widget.Entry
	onNext func()
	onPrev func()
	shift  bool
}

func newGridSearchEntry() *gridSearchEntry {
	e := &gridSearchEntry{}
	e.ExtendBaseWidget(e)
	return e
}

// MinSize asks for a fixed width; an Entry's natural width leaves the box
// unusable in an HBox.
func (e *gridSearchEntry) MinSize() fyne.Size {
	s := e.Entry.MinSize()
	s.Width = 200
	return s
}

func (e *gridSearchEntry) KeyDown(key *fyne.KeyEvent) {
	if key.Name == desktop.KeyShiftLeft || key.Name == desktop.KeyShiftRight {
		e.shift = true
	}
	e.Entry.KeyDown(key)
}

func (e *gridSearchEntry) KeyUp(key *fyne.KeyEvent) {
	if key.Name == desktop.KeyShiftLeft || key.Name == desktop.KeyShiftRight {
		e.shift = false
	}
	e.Entry.KeyUp(key)
}

func (e *gridSearchEntry) TypedKey(key *fyne.KeyEvent) {
	if key.Name == fyne.KeyReturn || key.Name == fyne.KeyEnter {
		if e.shift {
			if e.onPrev != nil {
				e.onPrev()
			}
		} else if e.onNext != nil {
			e.onNext()
		}
		return
	}
	e.Entry.TypedKey(key)
}
