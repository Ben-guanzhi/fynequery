package main

// DataTable: the main data grid modeled on TablePlus's table view —
// sticky header, row-number gutter, zebra rows, selected-row highlight,
// right-aligned numbers, dimmed NULL cells.

import (
	"image/color"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

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
	gutterWidth   float32 = 52 // row-number gutter
	rowHeight     float32 = 30
	headerH       float32 = 30
	minColWidth   float32 = 96
	virtualBuffer int     = 6 // extra rows kept above/below the viewport
)

type dataTable struct {
	widget.BaseWidget
	data   *TableData
	selRow int
	selCol int // active cell within selRow; drawn with a focus ring
	status *statusBar
	hidden map[int]bool // column indices hidden by the Columns dialog

	// manual column-width state: when frozen, colWidths holds the absolute pixel
	// width of every column (0 for hidden); otherwise columns auto-fit the view.
	frozen    bool
	colWidths []float32

	// editable-grid state (see edit.go)
	window      fyne.Window
	onDirty     func()
	onRowSelect func(row int) // fired when the selected row changes
	editable    bool
	pkCols      []int
	// kvEdit handles a double-click on a key/value grid and reports whether it
	// opened the element editor; nil for SQL sessions (see edit.go).
	kvEdit   func(row, col int) bool
	dirty    map[int]map[int]*string // row -> col -> new value (nil = NULL)
	deleted  map[int]bool            // rows marked for deletion
	inserted map[int]bool            // rows staged for insert (pending new rows)

	// column sort state (TablePlus HeaderSort / HeaderSortDirection)
	sortCol int
	sortAsc bool

	// Per-column values the cell renderers need on every paint, precomputed
	// once (see metaFor). Re-matching the SQL type and re-parsing the name's
	// magic suffix per cell per refresh was the grid's hottest cost.
	colMeta []colMeta
	// Column-width cache, keyed on widthsGen (bumped by every colWidths /
	// hidden / frozen mutation) and the requested width.
	cwCache   *colWidthsCache
	widthsGen int

	// in-result search state (TablePlus in-grid spotlight)
	findTerm     string
	findHits     []searchHitKey
	findHitSet   map[searchHitKey]bool
	findCur      int
	findTimer    *time.Timer // debounces the per-keystroke full scan
	findGen      int         // drops a debounced scan overtaken by newer typing
	onFindStatus func(cur, total int) // updates the counter label when hits change

	// copy/Query-Magics context: owning table name ("" for query results) and
	// the workspace hook behind __format__goto column links.
	tableName string
	onGoto    func(table, column, value string)
	onPin     func() // optional: pin this result (query pane)

	// keyboard/focus state: arrow-key row navigation + Ctrl+C copy of the
	// selected row. focused is set by FocusGained/Lost; Ctrl+C is registered
	// once per window canvas and guarded by it so only the active grid copies.
	focused       bool
	shortcutBound bool
	hoverRow      int // row under the pointer for hover highlight (-1 = none)

	rows *rowsWidget // the row area, kept so streamed rows can be appended
}

func newDataTable(d *TableData, status *statusBar) *dataTable {
	t := &dataTable{data: d, status: status, selRow: -1, selCol: -1, sortCol: -1, hoverRow: -1}
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
	t.widthsGen++
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
	t.selCol = -1
	t.sortCol = -1
	t.sortAsc = true
	t.findTerm = ""
	t.findHits = nil
	t.findHitSet = nil
	t.findCur = 0
	if t.findTimer != nil {
		t.findTimer.Stop()
		t.findTimer = nil
	}
	t.frozen = false
	t.colWidths = nil
	t.widthsGen++
	t.colMeta = nil
	if t.rows != nil {
		t.rows.rebuildRows()
	}
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
// sortKey is the precomputed comparison key for one row during a sort.
// Decorating once (O(n) ParseFloat calls) turns the sort's O(n log n)
// comparator into pure field lookups — re-parsing both cells per comparison
// made a 5000-row numeric sort parse ~120k times.
type sortKey struct {
	null  bool
	isNum bool
	num   float64
	s     string
}

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
	rows := t.data.Rows
	keys := make([]sortKey, len(rows))
	for i, row := range rows {
		v := cellStr(row, col)
		k := &keys[i]
		if v == nil {
			k.null = true
			continue
		}
		k.s = *v
		if numeric {
			if f, err := strconv.ParseFloat(k.s, 64); err == nil {
				k.num, k.isNum = f, true
			}
		}
	}
	// Decorate-sort-undecorate over an index permutation: the comparator
	// reads keys by ORIGINAL index while sort permutes `order`, then the
	// permutation is applied to the rows once. Sorting the row slice
	// directly would break the index↔key correspondence as elements move.
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := &keys[order[i]], &keys[order[j]]
		if a.null != b.null {
			return !a.null // non-NULL first; NULLs always sink to the bottom
		}
		if a.null {
			return false
		}
		if a.isNum && b.isNum {
			if asc {
				return a.num < b.num
			}
			return a.num > b.num
		}
		if asc {
			return a.s < b.s
		}
		return a.s > b.s
	})
	sorted := make([][]*string, len(rows))
	for i, src := range order {
		sorted[i] = rows[src]
	}
	t.data.Rows = sorted
	t.Refresh()
	t.rescanFind() // a re-order invalidates collected hit positions
}

// setFindTerm re-scans the grid for the term and spotlights the first hit.
// The scan is debounced: it touches every cell, so running it per keystroke
// over a large result froze typing. Clearing stays immediate so highlights
// vanish the moment the term is emptied.
func (t *dataTable) setFindTerm(term string) {
	t.findTerm = term
	t.findHits = nil
	t.findHitSet = nil
	t.findCur = 0
	if t.findTimer != nil {
		t.findTimer.Stop()
		t.findTimer = nil
	}
	if term == "" {
		t.updateFindStatus()
		t.Refresh()
		return
	}
	t.updateFindStatus()
	t.Refresh()
	t.findGen++
	gen := t.findGen
	t.findTimer = time.AfterFunc(findDebounce, func() {
		safeDo(func() {
			if gen != t.findGen || term != t.findTerm {
				return // typing continued; a newer scan is scheduled
			}
			t.applyFindTerm(term)
		})
	})
}

// findDebounce waits out fast typing before the whole-grid search scan runs.
const findDebounce = 180 * time.Millisecond

// applyFindTerm runs the (debounced) whole-grid scan for term synchronously:
// resets the hit list, rescans, spotlights the first hit and repaints.
func (t *dataTable) applyFindTerm(term string) {
	t.findHits = nil
	t.scanFindHits(strings.ToLower(term))
	t.buildFindSet()
	if len(t.findHits) > 0 {
		t.showFindHit(0)
	}
	t.updateFindStatus()
	t.Refresh()
}

// scanFindHits collects every cell whose displayed text contains the needle,
// in reading order. Comparing cellText means pending edits are searched too.
func (t *dataTable) scanFindHits(needle string) {
	t.scanFindHitsRange(0, len(t.data.Rows), needle)
}

// scanFindHitsRange collects hits in rows [from, to) — the incremental form
// appendRows uses while a result streams in.
func (t *dataTable) scanFindHitsRange(from, to int, needle string) {
	for r := from; r < to; r++ {
		for c := 0; c < len(t.data.Cols); c++ {
			if containsFold(t.cellText(r, c), needle) {
				t.findHits = append(t.findHits, searchHitKey{r, c})
			}
		}
	}
	t.buildFindSet()
}

// containsFold reports whether s contains needle, case-insensitively for
// ASCII and case-sensitively for non-ASCII text (CJK text is unaffected — it
// has no case). It allocates nothing, unlike Contains(ToLower(s), needle),
// which used to copy every cell's text on every keystroke.
func containsFold(s, needle string) bool {
	if needle == "" {
		return true
	}
	if strings.Contains(s, needle) {
		return true
	}
	if needle == strings.ToLower(needle) && needle == strings.ToUpper(needle) {
		return false // no ASCII letters in the needle: the exact match was the only chance
	}
	n := len(needle)
	for i := 0; i+n <= len(s); i++ {
		if asciiFoldEqual(s[i:i+n], needle) {
			return true
		}
	}
	return false
}

// asciiFoldEqual compares two equal-length byte strings ASCII-case-insensitively.
func asciiFoldEqual(a, b string) bool {
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca == cb {
			continue
		}
		if ('A' <= ca && ca <= 'Z') && cb == ca|0x20 {
			continue
		}
		if ('A' <= cb && cb <= 'Z') && ca == cb|0x20 {
			continue
		}
		return false
	}
	return true
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
	first := len(t.data.Rows)
	t.data.Rows = append(t.data.Rows, rows...)
	t.data.Fetched = len(t.data.Rows)
	if t.data.Fetched > t.data.Total {
		t.data.Total = t.data.Fetched
	}
	// The row views are built when the grid is painted; append the new ones
	// explicitly, otherwise a grid created empty (the query pane paints an
	// empty grid, then streams batches into it) would stay blank.
	if t.rows != nil {
		t.rows.syncRows()
	}
	t.Refresh()
	// While a term is active, scan only the newly arrived rows: rescanning
	// every cell per batch made streaming O(n²) with search on. Positions of
	// earlier hits are unchanged (rows only append), so the collected set
	// stays valid.
	if t.findTerm != "" {
		t.scanFindHitsRange(first, len(t.data.Rows), strings.ToLower(t.findTerm))
		t.updateFindStatus()
	}
}

// selectRow moves the row highlight and notifies the workspace so the right
// sidebar can show the row's field values. Only the affected rows repaint.
func (t *dataTable) selectRow(i int) {
	old, oldCol := t.selRow, t.selCol
	t.selRow = i
	if t.window != nil && t.window.Canvas() != nil {
		t.window.Canvas().Focus(t) // so arrow keys / Ctrl+C target this grid
	}
	if t.rows != nil {
		if old != i || oldCol != t.selCol {
			t.repaintRows(old, i) // repaint the rows whose highlight changed
		}
		t.rows.ensureVisible(i)
	} else {
		t.Refresh() // pre-layout: nothing to target yet
	}
	if t.onRowSelect != nil {
		t.onRowSelect(i)
	}
}

// repaintRows refreshes just the pooled row views bound to the given data
// rows (negative indices are skipped). Falls back to a full refresh when the
// row widget has not been laid out yet.
func (t *dataTable) repaintRows(rows ...int) {
	if t.rows == nil || t.rows.pool == nil {
		t.Refresh()
		return
	}
	repainted := false
	for _, rv := range t.rows.pool {
		for _, r := range rows {
			if r >= 0 && rv.idx == r && rv.Visible() {
				rv.Refresh()
				repainted = true
			}
		}
	}
	if !repainted && len(rows) > 0 {
		// None of the affected rows is on screen; nothing to do.
		_ = rows
	}
}

// selectCell moves the active-cell focus ring to (row, col) together with the
// row highlight, so the cell a click / edit / copy targets reads at a glance.
func (t *dataTable) selectCell(row, col int) {
	t.selCol = col
	t.selectRow(row)
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

// colWidthsCache holds the last columnWidths result together with the state
// it was computed from. The cached slice must be treated as read-only by
// callers — the header drag handler mutates t.colWidths directly and bumps
// widthsGen to discard the cache afterwards.
type colWidthsCache struct {
	gen   int
	avail float32
	out   []float32
}

func (t *dataTable) columnWidths(avail float32) []float32 {
	n := len(t.data.Cols)
	if n == 0 {
		return nil
	}
	if c := t.cwCache; c != nil && c.gen == t.widthsGen && c.avail == avail && len(c.out) == n {
		return c.out
	}
	var out []float32
	if t.frozen {
		out = make([]float32, n)
		for i := range out {
			if i < len(t.colWidths) {
				out[i] = t.colWidths[i]
			} else {
				out[i] = minColWidth
			}
		}
	} else {
		vis := t.visibleCount()
		if vis == 0 {
			vis = 1
		}
		w := (avail - gutterWidth) / float32(vis)
		if w < minColWidth {
			w = minColWidth
		}
		out = make([]float32, n)
		for i := range out {
			if t.isHidden(i) {
				out[i] = 0
				continue
			}
			out[i] = w
		}
	}
	t.cwCache = &colWidthsCache{gen: t.widthsGen, avail: avail, out: out}
	return out
}

// totalMinWidth is the full content width of the grid (gutter + all visible columns).
func (t *dataTable) totalMinWidth() float32 {
	if len(t.data.Cols) == 0 {
		return gutterWidth + 100
	}
	if t.frozen {
		var sum float32
		for i := range t.data.Cols {
			if i < len(t.colWidths) && !t.isHidden(i) {
				sum += t.colWidths[i]
			}
		}
		return gutterWidth + sum
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
	t.rows = rows
	inner := container.NewBorder(head, nil, nil, nil, rows)
	t.bindGridShortcuts()
	return widget.NewSimpleRenderer(container.NewHScroll(inner))
}

// ---- keyboard / focus ------------------------------------------------------

// FocusGained/FocusLost/TypedRune/TypedKey make the grid focusable so arrow
// keys and Ctrl+C reach it; only the focused grid's Ctrl+C acts.
func (t *dataTable) FocusGained()   { t.focused = true }
func (t *dataTable) FocusLost()     { t.focused = false }
func (t *dataTable) TypedRune(rune) {}

func (t *dataTable) TypedKey(e *fyne.KeyEvent) {
	n := len(t.data.Rows)
	if n == 0 {
		return
	}
	cur := t.selRow
	if cur < 0 {
		cur = 0
	}
	switch e.Name {
	case fyne.KeyUp:
		if cur > 0 {
			t.selectRow(cur - 1)
		}
	case fyne.KeyDown:
		if cur < n-1 {
			t.selectRow(cur + 1)
		}
	case fyne.KeyPageUp:
		t.selectRow(max(0, cur-20))
	case fyne.KeyPageDown:
		t.selectRow(min(n-1, cur+20))
	case fyne.KeyHome:
		t.selectRow(0)
	case fyne.KeyEnd:
		t.selectRow(n - 1)
	}
}

// bindGridShortcuts registers Ctrl+C once per window canvas to copy the
// selected row (tab-separated, spreadsheet-pasteable).
func (t *dataTable) bindGridShortcuts() {
	if t.shortcutBound || t.window == nil || t.window.Canvas() == nil {
		return
	}
	t.shortcutBound = true
	t.window.Canvas().AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyC, Modifier: fyne.KeyModifierControl},
		func(fyne.Shortcut) {
			if t.focused && t.selRow >= 0 && t.selRow < len(t.data.Rows) {
				t.copyText(t.rowTSV(t.selRow))
			}
		})
}

// rowTSV renders the selected row as tab-separated values for the clipboard.
func (t *dataTable) rowTSV(row int) string {
	vals := t.rowValues(row)
	fields := make([]string, len(t.data.Cols))
	for i := range t.data.Cols {
		fields[i] = cellPlain(vals, i)
	}
	return strings.Join(fields, "	")
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
	arrows    []*canvas.Text
	titles    []string  // full column titles, fitted to the column on layout
	colBounds []colSpan // visible column hit-test spans (local coords)
	dragCol   int       // column whose right edge is being dragged (-1 = none)
	hoverSep  bool      // pointer is over a column separator (resize cursor)
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
	h.titles = make([]string, len(h.t.data.Cols))
	for range h.t.data.Cols {
		l := canvas.NewText("", pal().gridHeaderFG)
		l.TextSize = 12
		l.TextStyle.Bold = true
		h.labels = append(h.labels, l)
		sep := canvas.NewRectangle(pal().line)
		h.seps = append(h.seps, sep)
		objs = append(objs, l, sep)
		ar := canvas.NewText("", pal().gridHeaderFG)
		ar.TextSize = 11
		h.arrows = append(h.arrows, ar)
		objs = append(objs, ar)
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
		var arrowW float32
		if r.h.t.sortCol == i && i < len(r.h.arrows) && r.h.arrows[i].Text != "" {
			arrowW = r.h.arrows[i].MinSize().Width + 6
		}
		// Header text is clipped to its column too: with many columns the
		// natural label width exceeds the column and the names would overlap.
		full := ""
		if i < len(r.h.titles) {
			full = r.h.titles[i]
		}
		if full == "" {
			full = r.h.t.colTitle(i)
		}
		l.Text = fitCellText(full, lw-16-arrowW, l.TextSize, l.TextStyle)
		lm := l.MinSize()
		if isNumericType(r.h.t.data.Cols[i].Type) {
			l.Move(fyne.NewPos(x+lw-14-arrowW-lm.Width, (headerH-lm.Height)/2))
		} else {
			l.Move(fyne.NewPos(x, (headerH-lm.Height)/2))
		}
		if i < len(r.h.arrows) {
			am := r.h.arrows[i].MinSize()
			r.h.arrows[i].Move(fyne.NewPos(x+lw-6-am.Width, (headerH-am.Height)/2))
			r.h.arrows[i].Resize(fyne.NewSize(am.Width+2, headerH))
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

// columnAtX returns the index of the column whose right separator lies within a
// few px of x, or -1. Used to pick which column a drag/resize is acting on.
func (h *headerWidget) columnAtX(x float32) int {
	widths := h.t.columnWidths(h.Size().Width)
	cx := gutterWidth
	for i := range widths {
		if h.t.isHidden(i) {
			continue
		}
		cx += widths[i]
		if math.Abs(float64(x-cx)) <= 6 {
			return i
		}
	}
	return -1
}

// Dragged resizes the column under the pointer. The first move freezes the
// current widths (so columns stop auto-fitting and stay where you put them);
// later drags keep mutating the stored width and re-laying out live.
func (h *headerWidget) Dragged(e *fyne.DragEvent) {
	t := h.t
	if h.dragCol < 0 {
		h.dragCol = h.columnAtX(e.Position.X)
		// The column set can change under an in-flight drag (a reload landing
		// mid-gesture); drop the drag if the resolved column no longer exists.
		if h.dragCol < 0 || h.dragCol >= len(t.data.Cols) {
			h.dragCol = -1
			return
		}
	}
	// Freeze the current widths, and (re)build the stored slice so it spans
	// every column. frozen can be true with a stale/short colWidths (e.g. the
	// column count grew since it was captured); columnWidths() pads to the
	// current length, so this guarantees the index below is in range.
	if !t.frozen || len(t.colWidths) < len(t.data.Cols) {
		t.colWidths = t.columnWidths(h.Size().Width)
		t.frozen = true
	}
	if h.dragCol >= len(t.colWidths) {
		return // defensive: should be unreachable after the rebuild above
	}
	t.colWidths[h.dragCol] += e.Dragged.DX
	if t.colWidths[h.dragCol] < minColWidth {
		t.colWidths[h.dragCol] = minColWidth
	}
	// The drag mutates t.colWidths (which may alias the width cache), so the
	// cache must go.
	t.widthsGen++
	t.Refresh()
	t.Resize(t.Size())
}

func (h *headerWidget) DragEnd() { h.dragCol = -1 }

func (h *headerWidget) MouseIn(*desktop.MouseEvent) { h.updateResizeCursor() }
func (h *headerWidget) MouseOut()                   { h.hoverSep = false }
func (h *headerWidget) MouseMoved(e *desktop.MouseEvent) {
	h.hoverSep = h.columnAtX(e.Position.X) >= 0
}

// updateResizeCursor refreshes the resize cursor after MouseIn (which lacks a
// position); the live position is handled by MouseMoved.
func (h *headerWidget) updateResizeCursor() {}

// Cursor shows a horizontal-resize pointer when hovering a column separator.
func (h *headerWidget) Cursor() desktop.Cursor {
	if h.hoverSep {
		return desktop.HResizeCursor
	}
	return desktop.DefaultCursor
}

// DoubleTapped on the header resets every column back to auto-fit.
func (h *headerWidget) DoubleTapped(*fyne.PointEvent) {
	t := h.t
	t.frozen = false
	t.colWidths = nil
	t.widthsGen++
	t.Refresh()
	t.Resize(t.Size())
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
		r.h.titles[i] = r.h.t.colTitle(i)
		// TablePlus highlights primary-key column headers in gold.
		if r.h.t.data.Cols[i].Key == "PK" {
			l.Color = p.pkHeader
		} else {
			l.Color = p.gridHeaderFG
		}
		// Sort arrow is a separate glyph so a narrow column's ellipsis can never
		// clip it (it used to be appended to the title text).
		if i < len(r.h.arrows) {
			if r.h.t.sortCol == i {
				if r.h.t.sortAsc {
					r.h.arrows[i].Text = "▲"
				} else {
					r.h.arrows[i].Text = "▼"
				}
				r.h.arrows[i].Color = l.Color
				r.h.arrows[i].Show()
			} else {
				r.h.arrows[i].Text = ""
				r.h.arrows[i].Hide()
			}
			r.h.arrows[i].Refresh()
		}
		l.Refresh()
		r.h.seps[i].FillColor = p.gridLine
		r.h.seps[i].Refresh()
	}
	// Re-fit the (possibly changed) titles and re-place the numeric headers.
	r.Layout(r.h.Size())
}

func (r *headerRenderer) Destroy() {}

// ---------------------------------------------------------------------------
// rowsWidget: a vertically scrollable, windowed (virtualized) row view.
//
// Only the rows currently in (or near) the viewport exist as widgets — a small
// pool is recycled as the user scrolls, instead of instantiating one widget per
// data row. The scroll content holds an invisible "extent" rectangle sized to
// the full result set (so the scrollbar range is correct) plus the recycled
// pool, each pool row positioned at its true absolute y = dataIdx*rowHeight.
// Fyne's scroll translates the whole content, so a pool row bound to data row K
// naturally appears at screen y = K*rowHeight - offset.

// extentWidget is an invisible spacer that reports the full scrollable size to
// the scroll container through MinSize. A canvas.Rectangle reports a zero
// MinSize and Container.MinSize() only takes the max of its children's MinSize
// (not their positions), so the content used to measure one row tall — leaving
// the mouse wheel and scrollbar with no range to move through.
type extentWidget struct {
	widget.BaseWidget
	min fyne.Size
}

func newExtentWidget() *extentWidget {
	e := &extentWidget{}
	e.ExtendBaseWidget(e)
	return e
}

func (e *extentWidget) MinSize() fyne.Size { return e.min }

func (e *extentWidget) setSize(w, h float32) {
	s := fyne.NewSize(w, h)
	if s == e.min {
		return
	}
	e.min = s
	e.Resize(s)
	e.Refresh()
}

func (e *extentWidget) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

type rowsWidget struct {
	widget.BaseWidget
	t            *dataTable
	scroll       *container.Scroll
	content      *fyne.Container // AbsoluteLayout: extent + pool rows
	extent       *extentWidget
	pool         []*rowView
	offsetY      float32
	contentWidth float32
	empty        *widget.Label // centered no-rows / loading overlay
}

func newRowsWidget(t *dataTable) *rowsWidget {
	rw := &rowsWidget{t: t}
	rw.ExtendBaseWidget(rw)
	return rw
}

func (rw *rowsWidget) CreateRenderer() fyne.WidgetRenderer {
	rw.contentWidth = rw.t.totalMinWidth()
	rw.extent = newExtentWidget()
	rw.content = container.NewWithoutLayout(rw.extent)
	rw.scroll = container.NewVScroll(rw.content)
	// Reposition the recycled pool whenever the user scrolls.
	rw.scroll.OnScrolled = func(pos fyne.Position) {
		rw.offsetY = pos.Y
		rw.reposition()
	}
	rw.empty = widget.NewLabel("")
	rw.empty.Alignment = fyne.TextAlignCenter
	rw.empty.Wrapping = fyne.TextWrapWord
	rw.empty.Importance = widget.LowImportance
	stack := container.NewStack(rw.scroll, container.NewCenter(rw.empty))
	rw.rebuildRows()
	return &rowsWidgetRenderer{rw: rw, stack: stack}
}

// rowsWidgetRenderer lays the scroll widget out to the allotted size and keeps
// the recycled pool in sync on every resize/refresh.
type rowsWidgetRenderer struct {
	rw    *rowsWidget
	stack *fyne.Container
}

func (r *rowsWidgetRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.stack} }
func (r *rowsWidgetRenderer) Destroy()                     {}
func (r *rowsWidgetRenderer) MinSize() fyne.Size           { return r.rw.MinSize() }

func (r *rowsWidgetRenderer) Layout(size fyne.Size) {
	r.stack.Resize(size)
	r.rw.reposition()
}

func (r *rowsWidgetRenderer) Refresh() { r.rw.reposition() }

// rebuildRows resets the recycled pool for a wholesale data (or column) change
// and rebuilds the visible window from scratch.
func (rw *rowsWidget) rebuildRows() {
	rw.pool = nil
	rw.offsetY = 0
	if rw.scroll != nil {
		rw.scroll.ScrollToTop()
	}
	if rw.content != nil {
		rw.content.Objects = rw.content.Objects[:0]
		rw.content.Add(rw.extent)
	}
	rw.reposition()
}

// syncRows brings the visible window in line with the data after a streamed
// batch or a shrink: the pool is (re)bound in reposition(), so nothing here but
// a re-layout is needed.
func (rw *rowsWidget) syncRows() {
	rw.reposition()
}

// ensureVisible scrolls the given data row into view (arrow-key navigation),
// so the selection never moves off-screen.
func (rw *rowsWidget) ensureVisible(idx int) {
	if rw.scroll == nil || idx < 0 {
		return
	}
	vpH := rw.scroll.Size().Height
	if vpH <= 0 {
		vpH = 600
	}
	top := float32(idx) * rowHeight
	bottom := top + rowHeight
	off := rw.offsetY
	if top < off {
		off = top
	} else if bottom > off+vpH {
		off = bottom - vpH
	}
	if off < 0 {
		off = 0
	}
	if off != rw.offsetY {
		rw.offsetY = off
		rw.scroll.Offset.Y = off
		rw.scroll.Refresh()
	}
	rw.reposition()
}

func (rw *rowsWidget) MinSize() fyne.Size {
	return fyne.NewSize(rw.t.totalMinWidth(), rowHeight*4)
}

// reposition (re)binds the recycled pool to the data rows currently in view.
// It is safe to call on scroll, resize and data changes.
func (rw *rowsWidget) reposition() {
	if rw.content == nil || rw.extent == nil {
		return
	}
	n := len(rw.t.data.Rows)
	width := rw.t.totalMinWidth()
	if vp := rw.scroll.Size().Width; vp > width {
		width = vp
	}
	rw.contentWidth = width
	rw.extent.setSize(width, float32(n)*rowHeight)

	if n == 0 || len(rw.t.data.Cols) == 0 {
		for _, rv := range rw.pool {
			rv.Hide()
		}
		if rw.empty != nil {
			if len(rw.t.data.Cols) == 0 {
				rw.empty.SetText("加载中…")
			} else {
				rw.empty.SetText("暂无数据")
			}
			rw.empty.Show()
		}
		return
	}
	if rw.empty != nil {
		rw.empty.Hide()
	}

	vpH := int(rw.scroll.Size().Height)
	if vpH <= 0 {
		vpH = 600 // pre-layout fallback; corrected on first Layout
	}
	first, last := visibleWindow(int(rw.offsetY), vpH, int(rowHeight), n, virtualBuffer)
	poolSize := last - first + 1
	if poolSize < 1 {
		poolSize = 1
	}

	// Grow the pool if the viewport now needs more row views than we have.
	for len(rw.pool) < poolSize {
		rv := newRowView(rw.t, 0)
		rw.content.Add(rv)
		rw.pool = append(rw.pool, rv)
	}
	// Hide surplus pool entries (e.g. after the result set shrank).
	for k := poolSize; k < len(rw.pool); k++ {
		rw.pool[k].Hide()
	}

	for k := 0; k < poolSize; k++ {
		idx := first + k
		rv := rw.pool[k]
		if idx >= n {
			rv.Hide()
			continue
		}
		if rv.idx != idx {
			rv.bindIndex(idx)
		} else {
			rv.Refresh() // pick up hidden-column / selection / dirty updates
		}
		rv.Resize(fyne.NewSize(width, rowHeight))
		rv.Move(fyne.NewPos(0, float32(idx)*rowHeight))
		rv.Show()
	}
}

// visibleWindow returns the inclusive [first,last] range of data rows that the
// recycled pool must cover for a scroll offset of offsetY within a viewport of
// viewportH pixels, given row height rowH, total row count count and a buffer
// of extra rows kept above/below. Pure and unit-tested.
func visibleWindow(offsetY, viewportH, rowH, count, buffer int) (first, last int) {
	if count <= 0 || rowH <= 0 {
		return 0, -1
	}
	vis := (viewportH + rowH - 1) / rowH // ceil(viewportH/rowH)
	if vis < 1 {
		vis = 1
	}
	poolSize := vis + 2*buffer
	if poolSize < 1 {
		poolSize = 1
	}

	first = offsetY / rowH
	if first < 0 {
		first = 0
	}
	first -= buffer
	if first < 0 {
		first = 0
	}

	last = first + poolSize - 1
	if last >= count {
		// Scrolled past the end: pin the window to the bottom poolSize rows.
		last = count - 1
		first = last - poolSize + 1
		if first < 0 {
			first = 0
		}
	}
	if last < first {
		last = first // safety net (e.g. empty window)
	}
	return first, last
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

// bindIndex rebinds this (recycled) row view to a different data row. The cells
// read their values from t.data at (row,col) on refresh, so re-pointing the
// stored index and refreshing is enough — no widget rebuild needed. Used by the
// virtualized rowsWidget, which keeps a small pool of row views and reuses them
// for whatever slice of the data is currently in view.
func (rv *rowView) bindIndex(idx int) {
	if len(rv.cells) != len(rv.t.data.Cols) {
		// Column set changed (setData with a different table): rebuild cells.
		n := len(rv.t.data.Cols)
		rv.cells = make([]*cellView, n)
		for ci := 0; ci < n; ci++ {
			rv.cells[ci] = newCellView(rv.t, ci, idx)
		}
	} else {
		for _, c := range rv.cells {
			c.row = idx
		}
	}
	rv.idx = idx
	rv.Refresh()
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

// MouseIn/Out drive the row hover highlight via dataTable.hoverRow, read by the
// gutter and cell renderers. Only the two affected row views repaint — a full
// dataTable.Refresh walks every pooled row (~1000 cells) and made moving the
// mouse down a column stutter.
func (rv *rowView) MouseIn(*desktop.MouseEvent) {
	if rv.t.hoverRow != rv.idx {
		old := rv.t.hoverRow
		rv.t.hoverRow = rv.idx
		rv.t.repaintRows(old, rv.idx)
	}
}
func (rv *rowView) MouseMoved(*desktop.MouseEvent) {}
func (rv *rowView) MouseOut() {
	if rv.t.hoverRow == rv.idx {
		rv.t.hoverRow = -1
		rv.t.repaintRows(rv.idx)
	}
}

func (rv *rowView) CreateRenderer() fyne.WidgetRenderer {
	rv.gut = canvas.NewText(strconv.Itoa(rv.idx+1), pal().gridNullFG)
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
	r.rv.gut.Text = strconv.Itoa(r.rv.idx+1)
	p := pal()
	if r.rv.idx == r.rv.t.selRow {
		r.rv.gut.Color = p.selFG // white row number on the blue selection
	} else {
		r.rv.gut.Color = p.gridNullFG
	}
	r.rv.gutBG.FillColor = p.gutterBG
	switch {
	case r.rv.idx == r.rv.t.selRow:
		r.rv.gutBG.FillColor = p.gridRowSelectBG
	case r.rv.idx == r.rv.t.hoverRow:
		r.rv.gutBG.FillColor = p.gridRowHoverBG
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
	ring := canvas.NewRectangle(color.Transparent)
	ring.StrokeWidth = 2
	ring.Hide()
	return &cellRenderer{cv: cv, bg: bg, text: text, ring: ring}
}

type cellRenderer struct {
	cv   *cellView
	bg   *canvas.Rectangle
	text *canvas.Text
	ring *canvas.Rectangle // active-cell focus outline

	// fit cache: the value before clipping, the width it was fitted to and the
	// style used. A repaint with the same values skips re-measuring.
	rawText  string
	fitRaw   string
	fitWidth float32
	fitStyle fyne.TextStyle
}

func (r *cellRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.text, r.ring}
}

func (r *cellRenderer) Layout(size fyne.Size) {
	r.bg.Resize(fyne.NewSize(size.Width-1, size.Height))
	r.text.Resize(fyne.NewSize(size.Width-24, size.Height))
	r.ring.Resize(fyne.NewSize(size.Width-1, size.Height-1))
	r.fitText()
}

// fitText clips the cell's raw value to the current width. canvas.Text neither
// wraps nor clips, so an overlong value would otherwise paint over the
// neighbouring columns and rows.
func (r *cellRenderer) fitText() {
	w := r.cv.Size().Width - 24
	if w < 0 {
		w = 0
	}
	if r.fitRaw == r.rawText && r.fitWidth == w && r.fitStyle == r.text.TextStyle {
		return
	}
	r.fitRaw, r.fitWidth, r.fitStyle = r.rawText, w, r.text.TextStyle
	r.text.Text = fitCellText(r.rawText, w, r.text.TextSize, r.text.TextStyle)
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
	m := cv.t.metaFor(cv.col)
	numeric := m.numeric

	txt := ""
	style := fyne.TextStyle{}
	if isNull {
		txt = "NULL"
		r.text.Color = p.gridNullFG
		style.Italic = true
	} else {
		// TablePlus color-codes cell values by column type; Query Magics
		// (__format__ aliases) reshape the text/colour first.
		raw := *cell
		txt = raw
		r.text.Color = cellColor(m, p)
		if strings.HasPrefix(m.magic, "goto:") {
			// a table-jump link (TablePlus foreign-key arrow)
			r.text.Color = p.statusLink
			style.Italic = true
		} else if m.magic != "" {
			if mtxt, c, mstyle, ok := renderMagic(m.magic, raw, p); ok {
				txt = mtxt
				r.text.Color = c
				style = mstyle
			}
		}
	}
	if edited {
		r.text.Color = p.cellEdited
	}
	if cv.row == cv.t.selRow {
		r.text.Color = p.selFG // white values on the blue selected row
	}
	if numeric && !isNull {
		r.text.Alignment = fyne.TextAlignTrailing
	} else {
		r.text.Alignment = fyne.TextAlignLeading
	}
	if cv.t.isFindHit(cv.row, cv.col) {
		r.text.Color = findHighlightFG
		style.Bold = true
	}
	r.text.TextStyle = style
	r.rawText = txt
	r.fitText()
	switch {
	case cv.t.deleted[cv.row]:
		r.bg.FillColor = p.rowDeletedBG
	case cv.row == cv.t.selRow:
		r.bg.FillColor = p.gridRowSelectBG
	case cv.t.inserted[cv.row]:
		r.bg.FillColor = p.rowNewBG
	case cv.row == cv.t.hoverRow:
		r.bg.FillColor = p.gridRowHoverBG
	case cv.t.isFindHit(cv.row, cv.col):
		r.bg.FillColor = findHighlightBG
	case cv.row%2 == 1:
		r.bg.FillColor = p.gridRowAltBG
	default:
		r.bg.FillColor = color.Transparent
	}
	if cv.t.selRow >= 0 && cv.row == cv.t.selRow && cv.col == cv.t.selCol {
		r.ring.StrokeColor = p.accent // active-cell focus outline
		r.ring.Show()
	} else {
		r.ring.Hide()
	}
	r.bg.Refresh()
	r.text.Refresh()
	r.ring.Refresh()
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
	cv.t.selectCell(cv.row, cv.col)
}

// DoubleTapped opens the cell editor when the grid is in edit mode; in
// read-only mode it opens the value viewer (Quick Look) so a JSON/text/blob
// cell can be inspected and copied without first entering edit mode.
func (cv *cellView) DoubleTapped(*fyne.PointEvent) {
	if cv.t.kvEdit != nil && cv.t.kvEdit(cv.row, cv.col) {
		return
	}
	if cv.t.editable {
		cv.t.beginEdit(cv.row, cv.col)
		return
	}
	if cv.t.window != nil {
		cv.t.quickLook(cv.row, cv.col)
	}
}

// colMeta carries the per-column values the cell renderers need on every
// paint, precomputed once per data change instead of re-derived per cell:
// cellColor's ToLower(Type) alone allocated one string per cell per refresh.
type colMeta struct {
	numeric  bool
	colorKey int // one of the ck* constants below
	base     string
	magic    string
}

// Colour kinds cellColor maps to palette entries (cellColor's former switch).
const (
	ckDefault = iota
	ckInt
	ckFloat
	ckDate
	ckStr
)

// metaFor returns the derived values for column col, rebuilding the cache
// when the column set changed. Lazy so construction order never matters.
func (t *dataTable) metaFor(col int) colMeta {
	if len(t.colMeta) != len(t.data.Cols) {
		t.rebuildColMeta()
	}
	if col < 0 || col >= len(t.colMeta) {
		return colMeta{}
	}
	return t.colMeta[col]
}

func (t *dataTable) rebuildColMeta() {
	t.colMeta = make([]colMeta, len(t.data.Cols))
	for i, c := range t.data.Cols {
		m := &t.colMeta[i]
		m.numeric = isNumericType(c.Type)
		m.base, m.magic = splitMagic(c.Name)
		switch strings.ToLower(c.Type) {
		case "int", "int2", "int4", "int8", "integer", "tinyint", "smallint",
			"mediumint", "bigint", "serial", "bigserial", "smallserial", "bit", "bool", "boolean":
			m.colorKey = ckInt
		case "numeric", "decimal", "float", "float4", "float8", "double",
			"double precision", "real", "money", "number":
			m.colorKey = ckFloat
		case "date", "datetime", "timestamp", "time", "year", "timestamptz":
			m.colorKey = ckDate
		case "char", "varchar", "text", "string", "enum", "uuid", "json",
			"jsonb", "nvarchar", "nchar", "longtext", "mediumtext":
			m.colorKey = ckStr
		}
	}
}

// cellColor maps a column's precomputed colour kind to the palette,
// mirroring TablePlus' color-coded data grid.
func cellColor(m colMeta, p *palette) color.Color {
	switch m.colorKey {
	case ckInt:
		return p.cellInt
	case ckFloat:
		return p.cellFloat
	case ckDate:
		return p.cellDate
	case ckStr:
		return p.cellStr
	}
	return p.cellDefault
}

func (r *cellRenderer) Destroy() {}

// singleLineText flattens a value onto one line. canvas.Text draws embedded
// newlines and tabs literally, so a pretty-printed JSON (or any multi-line
// text) would spill into the rows below instead of staying in its cell.
func singleLineText(s string) string {
	if !strings.ContainsAny(s, "\n\r\t") &&
		!strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			r = ' '
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		if r == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// fitCellText flattens s to a single line and ellipsizes it to width. Fyne's
// canvas.Text neither wraps nor clips, so an overlong value (JSON, blobs, long
// text) would otherwise paint across the neighbouring cells; TablePlus clips
// the value at the cell edge instead.
func fitCellText(s string, width, size float32, style fyne.TextStyle) string {
	s = singleLineText(s)
	if s == "" || width <= 0 {
		return ""
	}
	if textWidth(s, size, style) <= width {
		return s
	}
	r := []rune(s)
	if len(r) > 512 {
		// No cell is wide enough to show more than this; keeping the search
		// bounded stops a megabyte-sized value from costing log(n) full
		// measurements on every repaint.
		r = r[:512]
	}
	lo, hi := 0, len(r)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if textWidth(string(r[:mid])+"…", size, style) <= width {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 {
		return "…"
	}
	return string(r[:lo]) + "…"
}

// textWidth measures s in the current theme's font. Unit tests can run
// without an app; fall back to a rough estimate there so layout code never
// panics.
func textWidth(s string, size float32, style fyne.TextStyle) float32 {
	if fyne.CurrentApp() == nil {
		return float32(len([]rune(s))) * size * 0.6
	}
	return fyne.MeasureText(s, size, style).Width
}

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
