package main

import (
	"fmt"
	"strings"

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
	onNext func()
	onPrev func()
	shift  bool
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

// onGridSearchChanged re-scans the result for the term and spotlights the
// first hit.
func (c *Client) onGridSearchChanged(term string) {
	c.searchTerm = term
	c.searchHits = nil
	c.searchHitSet = nil
	c.searchCur = 0

	needle := strings.ToLower(term)
	if needle != "" {
		c.scanSearchHits(needle)
		c.buildHitSet()
		if len(c.searchHits) > 0 {
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
	if rows > len(c.gridRows) {
		rows = len(c.gridRows) // queued inserts read past the base result
	}
	for r := 0; r < rows; r++ {
		for col := 0; col < cols; col++ {
			text, _ := c.gridCellState(r, col)
			if strings.Contains(strings.ToLower(text), needle) {
				c.searchHits = append(c.searchHits, searchHitKey{r, col})
			}
		}
	}
}

// buildHitSet indexes the collected hits for the per-cell highlight test.
func (c *Client) buildHitSet() {
	c.searchHitSet = nil
	if len(c.searchHits) == 0 {
		return
	}
	c.searchHitSet = make(map[searchHitKey]bool, len(c.searchHits))
	for _, h := range c.searchHits {
		c.searchHitSet[h] = true
	}
}

// rescanGridSearch re-runs the scan after the grid's values or order changed
// (a queued edit, a sort) while a term is active, so highlights keep pointing
// at the cells they were collected for. It always refreshes the grid, so a
// caller about to Refresh can call this instead.
func (c *Client) rescanGridSearch() {
	if c.searchTerm != "" {
		c.searchHits = nil
		c.scanSearchHits(strings.ToLower(c.searchTerm))
		c.buildHitSet()
		c.searchCur = max(0, min(c.searchCur, len(c.searchHits)-1))
	}
	c.updateSearchDisplay()
}

// stepSearch moves the spotlight one hit forward or backward, wrapping.
func (c *Client) stepSearch(delta int) {
	if len(c.searchHits) == 0 {
		return
	}
	c.showSearchHit((c.searchCur + delta + len(c.searchHits)) % len(c.searchHits))
	c.updateSearchDisplay()
}

// showSearchHit selects hit k. Table.Select scrolls it into view and fires
// OnSelected, which fills the cell viewer, so no extra refresh is needed.
func (c *Client) showSearchHit(k int) {
	c.searchCur = k
	h := c.searchHits[k]
	if c.resultGrid != nil {
		c.resultGrid.Select(widget.TableCellID{Row: h[0], Col: h[1]})
	}
}

// updateSearchDisplay refreshes the hit counter and the grid highlights.
func (c *Client) updateSearchDisplay() {
	if c.searchHitsLabel != nil {
		if c.searchTerm == "" {
			c.searchHitsLabel.SetText("")
		} else if len(c.searchHits) == 0 {
			c.searchHitsLabel.SetText("0/0")
		} else {
			c.searchHitsLabel.SetText(fmt.Sprintf("%d/%d", c.searchCur+1, len(c.searchHits)))
		}
	}
	if c.resultGrid != nil {
		c.resultGrid.Refresh()
	}
}

// clearGridSearch drops the search box and its state: a new result invalidates
// every previous hit.
func (c *Client) clearGridSearch() {
	c.searchTerm = ""
	c.searchHits = nil
	c.searchHitSet = nil
	c.searchCur = 0
	if c.gridSearchEntry != nil {
		c.gridSearchEntry.setTextSilently("")
	}
	if c.searchHitsLabel != nil {
		c.searchHitsLabel.SetText("")
	}
}

// isSearchHit reports whether the cell matches the active search.
func (c *Client) isSearchHit(row, col int) bool {
	if c.searchTerm == "" || c.searchHitSet == nil {
		return false
	}
	return c.searchHitSet[searchHitKey{row, col}]
}
