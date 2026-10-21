package main

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// Result-set search: a box on the cell bar scans the displayed grid,
// highlights every hit, and Enter/Shift+Enter walks the hits. The grid
// selection follows the hit, so the full value appears in the cell viewer.

// searchHitKey packs a cell position into one map key.
type searchHitKey [2]int

// searchEntry is the result-search box. Plain Enter advances to the next hit,
// Shift+Enter goes back; both are swallowed so no newline is inserted. Fyne
// does not deliver modifiers through TypedKey, so shift state is tracked from
// KeyDown/KeyUp, which the driver calls on the focused widget first.
type searchEntry struct {
	widget.Entry
	onNext     func()
	onPrev     func()
	onShortcut func(fyne.Shortcut) bool
	shift      bool
}

func newSearchEntry() *searchEntry {
	e := &searchEntry{}
	// Route renderer/refresh calls through the outer type, as the embedded
	// Entry expects when it is wrapped (same as suggestEntry).
	e.ExtendBaseWidget(e)
	return e
}

// An Entry's natural MinSize width is barely wider than its cursor, which in
// an HBox leaves the box unusable, so the search box asks for a fixed width.
const gridSearchWidth = 200.0

func (e *searchEntry) MinSize() fyne.Size {
	s := e.Entry.MinSize()
	s.Width = gridSearchWidth
	return s
}

func (e *searchEntry) KeyDown(key *fyne.KeyEvent) {
	if key.Name == desktop.KeyShiftLeft || key.Name == desktop.KeyShiftRight {
		e.shift = true
	}
	e.Entry.KeyDown(key)
}

func (e *searchEntry) KeyUp(key *fyne.KeyEvent) {
	if key.Name == desktop.KeyShiftLeft || key.Name == desktop.KeyShiftRight {
		e.shift = false
	}
	e.Entry.KeyUp(key)
}

func (e *searchEntry) TypedKey(key *fyne.KeyEvent) {
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

// TypedShortcut claims the app-level combos (the search box is Shortcutable
// like any Entry, so the canvas would never see them) before stepping hits.
func (e *searchEntry) TypedShortcut(sc fyne.Shortcut) {
	if e.onShortcut != nil && e.onShortcut(sc) {
		return
	}
	e.Entry.TypedShortcut(sc)
}

// setTextSilently clears the box without firing the change callback, so a
// program fill does not trigger a second scan and grid refresh.
func (e *searchEntry) setTextSilently(text string) {
	if e.Text == text {
		return
	}
	cb := e.Entry.OnChanged
	e.Entry.OnChanged = nil
	e.Entry.SetText(text)
	e.Entry.OnChanged = cb
}

// searchDebounceDelay / searchDebounceThreshold gate the debounced rescan: only
// a result at least this large defers its rescan, and only by this long. Small
// results - and every test, which leaves searchDelay at 0 - rescan synchronously,
// so behaviour is unchanged for the common case.
const (
	searchDebounceDelay     = 150 * time.Millisecond
	searchDebounceThreshold = 2000
)

// onGridSearchChanged reacts to a keystroke in the search box. On a small result
// it rescans immediately; on a large one it debounces, so typing does not walk
// every row per keystroke - the rescan runs once typing pauses.
func (c *Client) onGridSearchChanged(term string) {
	c.find.term = term
	c.find.cur = 0
	if c.searchDelay > 0 && len(c.grid.rows) >= searchDebounceThreshold {
		if c.find.timer != nil {
			c.find.timer.Stop()
		}
		delay := c.searchDelay
		c.find.timer = time.AfterFunc(delay, func() {
			c.dispatch(c.runSearchScan)
		})
		return
	}
	c.runSearchScan()
}

// runSearchScan collects the hits for the current term and spotlights the first.
func (c *Client) runSearchScan() {
	c.find.hits = nil
	c.find.hitSet = nil

	needle := strings.ToLower(c.find.term)
	if needle != "" {
		c.scanSearchHits(needle)
		c.buildHitSet()
		if len(c.find.hits) > 0 {
			c.showSearchHit(0)
		}
	}
	c.updateSearchDisplay()
}

// scanSearchHits collects every cell whose displayed text contains the needle,
// in reading order. Comparing gridCellState means pending edits are searched
// too, and the lowercase pass is what makes the match case-insensitive.
func (c *Client) scanSearchHits(needle string) {
	rows, cols := c.gridDims()
	if rows > len(c.grid.rows) {
		rows = len(c.grid.rows) // queued inserts read past the base result
	}
	for r := 0; r < rows; r++ {
		for col := 0; col < cols; col++ {
			text, _ := c.gridCellState(r, col)
			if strings.Contains(strings.ToLower(text), needle) {
				c.find.hits = append(c.find.hits, searchHitKey{r, col})
			}
		}
	}
}

// buildHitSet indexes the collected hits for the per-cell highlight test.
func (c *Client) buildHitSet() {
	c.find.hitSet = nil
	if len(c.find.hits) == 0 {
		return
	}
	c.find.hitSet = make(map[searchHitKey]bool, len(c.find.hits))
	for _, h := range c.find.hits {
		c.find.hitSet[h] = true
	}
}

// rescanGridSearch re-runs the scan after the grid's values or order changed
// (a queued edit, a sort) while a term is active, so highlights keep pointing
// at the cells they were collected for. It always refreshes the grid, so a
// caller about to Refresh can call this instead.
func (c *Client) rescanGridSearch() {
	if c.find.term != "" {
		c.find.hits = nil
		c.scanSearchHits(strings.ToLower(c.find.term))
		c.buildHitSet()
		c.find.cur = max(0, min(c.find.cur, len(c.find.hits)-1))
	}
	c.updateSearchDisplay()
}

// stepSearch moves the spotlight one hit forward or backward, wrapping.
func (c *Client) stepSearch(delta int) {
	if len(c.find.hits) == 0 {
		return
	}
	c.showSearchHit((c.find.cur + delta + len(c.find.hits)) % len(c.find.hits))
	c.updateSearchDisplay()
}

// showSearchHit selects hit k. Table.Select scrolls it into view and fires
// OnSelected, which fills the cell viewer, so no extra refresh is needed.
func (c *Client) showSearchHit(k int) {
	c.find.cur = k
	h := c.find.hits[k]
	if c.grid.table != nil {
		c.grid.table.Select(widget.TableCellID{Row: h[0], Col: h[1]})
	}
}

// updateSearchDisplay refreshes the hit counter and the grid highlights.
func (c *Client) updateSearchDisplay() {
	if c.find.label != nil {
		if c.find.term == "" {
			c.find.label.SetText("")
		} else if len(c.find.hits) == 0 {
			c.find.label.SetText("0/0")
		} else {
			c.find.label.SetText(fmt.Sprintf("%d/%d", c.find.cur+1, len(c.find.hits)))
		}
	}
	if c.grid.table != nil {
		c.grid.table.Refresh()
	}
}

// clearGridSearch drops the search box and its state: a new result invalidates
// every previous hit.
func (c *Client) clearGridSearch() {
	if c.find.timer != nil {
		c.find.timer.Stop()
		c.find.timer = nil
	}
	c.find.term = ""
	c.find.hits = nil
	c.find.hitSet = nil
	c.find.cur = 0
	if c.find.entry != nil {
		c.find.entry.setTextSilently("")
	}
	if c.find.label != nil {
		c.find.label.SetText("")
	}
}

// isSearchHit reports whether the cell matches the active search.
func (c *Client) isSearchHit(row, col int) bool {
	if c.find.term == "" || c.find.hitSet == nil {
		return false
	}
	return c.find.hitSet[searchHitKey{row, col}]
}
