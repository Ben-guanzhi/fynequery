package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// This file is the SQL editor module: the suggestEntry control (completion),
// the candidate-bar plumbing, the line-number gutter, and the query tabs that
// share one result grid.

// sqlKeywords seeds the completion pool; database and table names come from
// the live connection on top of these.
var sqlKeywords = []string{
	"SELECT", "FROM", "WHERE", "INSERT", "INTO", "VALUES", "UPDATE", "SET",
	"DELETE", "JOIN", "LEFT", "RIGHT", "INNER", "OUTER", "FULL", "ON",
	"GROUP BY", "ORDER BY", "HAVING", "LIMIT", "OFFSET", "UNION", "ALL",
	"DISTINCT", "AS", "AND", "OR", "NOT", "NULL", "IS", "IN", "BETWEEN",
	"LIKE", "EXISTS", "CASE", "WHEN", "THEN", "ELSE", "END", "CREATE",
	"TABLE", "VIEW", "INDEX", "DROP", "ALTER", "ADD", "PRIMARY KEY",
	"FOREIGN KEY", "REFERENCES", "DEFAULT", "TRUNCATE", "ASC", "DESC",
	"COUNT", "SUM", "AVG", "MIN", "MAX", "WITH", "RETURNING", "TOP",
	"SHOW", "DESCRIBE", "EXPLAIN",
}

// maxSuggestions caps the candidate bar height.
const maxSuggestions = 5

// wordSpan locates the identifier under the caret within one line.
type wordSpan struct {
	row, start, end int
	text            string
}

// suggestEntry wraps the SQL editor with completion. fyne ships no
// CompletionEntry, so key handling and word splicing live here; the candidate
// bar itself is rendered by the client below the editor. While candidates are
// visible, Up/Down move the selection, Enter accepts it and Esc dismisses.
//
// The Entry is VALUE-embedded and ExtendBaseWidget points the widget identity
// at the wrapper: with a pointer-embedded wrapper the theme/refresh plumbing
// keeps targeting the inner entry and, e.g., dark theme never reaches it.
type suggestEntry struct {
	widget.Entry
	suggest      []string
	sugSel       int
	onSugChanged func() // re-renders the candidate bar; wired by the client
	onLines      func() // re-syncs the line-number gutter; wired by the client
}

// newSuggestEntry builds a multi-line SQL editor with completion support.
func newSuggestEntry() *suggestEntry {
	s := &suggestEntry{}
	s.MultiLine = true
	s.ExtendBaseWidget(s)
	return s
}

// MinSize grows with the text's line count, so the editor fills the tab's
// scroller instead of scrolling internally; the gutter tracks it.
func (s *suggestEntry) MinSize() fyne.Size {
	editorMetrics()
	n := strings.Count(s.Text, "\n") + 1
	if n <= 1 {
		return fyne.NewSize(editorBaseW, editorBaseH)
	}
	return fyne.NewSize(editorBaseW, editorBaseH+float32(n-1)*editorLineH)
}

// editorMetrics measures the monospace text metrics once: the per-line step
// (from a two-line label delta) and the editor's one-line base size, which
// the growing MinSize and the gutter both rely on.
var (
	editorMetricsOnce sync.Once
	editorLineH       float32 // advance of one text line
	editorBaseH       float32 // editor min height at one line
	editorBaseW       float32 // editor min width
)

func editorMetrics() {
	editorMetricsOnce.Do(func() {
		l1 := widget.NewLabel("Mg")
		l1.TextStyle = fyne.TextStyle{Monospace: true}
		l2 := widget.NewLabel("Mg\nMg")
		l2.TextStyle = fyne.TextStyle{Monospace: true}
		editorLineH = l2.MinSize().Height - l1.MinSize().Height
		probe := widget.NewMultiLineEntry()
		probe.TextStyle = fyne.TextStyle{Monospace: true}
		editorBaseH = probe.MinSize().Height
		editorBaseW = probe.MinSize().Width
		if editorLineH <= 0 {
			editorLineH = fyne.MeasureText("Mg", theme.TextSize(), fyne.TextStyle{Monospace: true}).Height
		}
	})
}

// TypedKey intercepts navigation keys while the candidate bar is open and
// forwards everything else to the entry.
func (s *suggestEntry) TypedKey(k *fyne.KeyEvent) {
	if len(s.suggest) > 0 {
		switch k.Name {
		case fyne.KeyDown:
			if s.sugSel < len(s.suggest)-1 {
				s.sugSel++
				s.notifySug()
			}
			return
		case fyne.KeyUp:
			if s.sugSel > 0 {
				s.sugSel--
				s.notifySug()
			}
			return
		case fyne.KeyEnter, fyne.KeyReturn:
			s.acceptSuggestion()
			return
		case fyne.KeyEscape:
			s.setSuggestions(nil)
			return
		}
	}
	s.Entry.TypedKey(k)
}

// setTextSilently replaces the editor text without firing the completion
// pass: the change callback is detached for the duration, so a program fill
// (tree click, history, filter) never looks like typing.
func (s *suggestEntry) setTextSilently(text string) {
	if text == s.Entry.Text {
		return
	}
	cb := s.OnChanged
	s.OnChanged = nil
	s.Entry.SetText(text)
	s.OnChanged = cb
	if s.onLines != nil {
		s.onLines()
	}
}

func (s *suggestEntry) notifySug() {
	if s.onSugChanged != nil {
		s.onSugChanged()
	}
}

func (s *suggestEntry) setSuggestions(list []string) {
	s.suggest = list
	s.sugSel = 0
	s.notifySug()
}

func (s *suggestEntry) acceptSuggestion() {
	if len(s.suggest) == 0 || s.sugSel >= len(s.suggest) {
		return
	}
	choice := s.suggest[s.sugSel]
	w := s.currentWord()
	if w.text == "" {
		return
	}
	cb := s.OnChanged
	s.OnChanged = nil
	s.Entry.SetText(replaceWord(s.Entry.Text, w, choice))
	s.OnChanged = cb
	s.Entry.CursorRow = w.row
	s.Entry.CursorColumn = w.start + utf8.RuneCountInString(choice)
	s.setSuggestions(nil)
	if s.onLines != nil {
		s.onLines()
	}
}

// currentWord returns the identifier under the caret, if any.
func (s *suggestEntry) currentWord() wordSpan {
	row, col := s.CursorRow, s.CursorColumn
	lines := strings.Split(s.Entry.Text, "\n")
	if row < 0 || row >= len(lines) {
		return wordSpan{}
	}
	line := []rune(lines[row])
	if col > len(line) {
		col = len(line)
	}
	start, end := wordBounds(line, col)
	return wordSpan{row: row, start: start, end: end, text: string(line[start:end])}
}

// wordBounds expands from col in both directions over identifier characters.
func wordBounds(line []rune, col int) (start, end int) {
	isWord := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
	}
	start = col
	for start > 0 && isWord(line[start-1]) {
		start--
	}
	end = col
	for end < len(line) && isWord(line[end]) {
		end++
	}
	return
}

// replaceWord splices repl over the word span in text.
func replaceWord(text string, w wordSpan, repl string) string {
	lines := strings.Split(text, "\n")
	line := []rune(lines[w.row])
	lines[w.row] = string(line[:w.start]) + repl + string(line[w.end:])
	return strings.Join(lines, "\n")
}

// matchSuggestions filters pool by case-insensitive prefix, dropping exact
// matches and duplicates, capped to keep the bar compact.
func matchSuggestions(pool []string, word string) []string {
	lw := strings.ToLower(word)
	seen := make(map[string]bool, len(pool))
	var out []string
	for _, p := range pool {
		if p == "" {
			continue
		}
		lp := strings.ToLower(p)
		if lp == lw || !strings.HasPrefix(lp, lw) || seen[lp] {
			continue
		}
		seen[lp] = true
		out = append(out, p)
		if len(out) >= maxSuggestions {
			break
		}
	}
	sort.Strings(out)
	return out
}

// newQueryEntry builds one tab's SQL editor; completion only reacts while its
// own tab is the active one. SQL renders monospace so the line-number gutter
// aligns with the editor's lines exactly.
func (c *Client) newQueryEntry() *suggestEntry {
	e := newSuggestEntry()
	e.SetPlaceHolder("在此输入 SQL …… 按 Ctrl+Enter 执行")
	e.TextStyle = fyne.TextStyle{Monospace: true}
	e.onSugChanged = c.renderSuggestions
	e.onLines = func() { c.syncTabMetrics(e) }
	e.OnChanged = func(string) {
		if c.queryEntry == e {
			c.onQueryTextChanged()
		}
		c.markTabDirty(e)
	}
	return e
}

// addQueryTab appends a fresh editor tab and makes it active. The tab's
// content pairs the SQL editor with a disabled line-number entry of the same
// metrics; both sit in the tab's own vertical scroller so long scripts
// scroll instead of stretching the window.
func (c *Client) addQueryTab() {
	c.tabSeq++
	e := c.newQueryEntry()
	gutter := widget.NewMultiLineEntry()
	gutter.TextStyle = fyne.TextStyle{Monospace: true}
	gutter.Wrapping = fyne.TextWrapOff
	gutter.Disable()
	row := container.NewBorder(nil, nil, gutter, nil, e)
	scroller := container.NewVScroll(row)
	item := container.NewTabItem(fmt.Sprintf("查询 %d", c.tabSeq), scroller)
	c.qItems = append(c.qItems, &queryTab{item: item, entry: e, gutter: gutter, scroller: scroller, baseTitle: item.Text})
	c.qTabs.Append(item)
	c.qTabs.Select(item)
	// fyne 2.8 auto-selects a first tab without firing OnSelected; make the
	// active-editor alias explicit either way.
	c.syncActiveTab(item)
	c.syncTabMetrics(e)
}

// markTabDirty flags a tab whose text changed since it last ran.
func (c *Client) markTabDirty(e *suggestEntry) {
	for _, t := range c.qItems {
		if t.entry == e && !t.dirty {
			t.dirty = true
			t.item.Text = t.baseTitle + " ●"
			c.qTabs.Refresh()
			return
		}
	}
}

// clearTabDirty clears the modified marker once the tab's text was executed.
func (c *Client) clearTabDirty(e *suggestEntry) {
	for _, t := range c.qItems {
		if t.entry == e && t.dirty {
			t.dirty = false
			t.item.Text = t.baseTitle
			c.qTabs.Refresh()
			return
		}
	}
}

// tabByItem finds the bookkeeping record of a tab-strip item.
func (c *Client) tabByItem(item *container.TabItem) *queryTab {
	for _, t := range c.qItems {
		if t.item == item {
			return t
		}
	}
	return nil
}

// closeActiveTab removes the current tab, always keeping one alive. fyne
// adjusts its selection silently on Remove, so the active-editor alias is
// re-synced here explicitly. A tab with unexecuted edits asks first.
func (c *Client) closeActiveTab() {
	if len(c.qItems) <= 1 {
		return
	}
	item := c.qTabs.CurrentTab()
	tab := c.tabByItem(item)
	if tab != nil && tab.dirty {
		dialog.ShowConfirm("关闭标签",
			"该标签的 SQL 已修改但尚未执行，确定关闭吗？",
			func(ok bool) {
				if ok {
					c.removeTab(item)
				}
			}, c.win)
		return
	}
	c.removeTab(item)
}

// removeTab drops one tab and re-syncs the active editor.
func (c *Client) removeTab(item *container.TabItem) {
	for i, t := range c.qItems {
		if t.item == item {
			c.qItems = append(c.qItems[:i], c.qItems[i+1:]...)
			break
		}
	}
	c.qTabs.Remove(item)
	if len(c.qItems) > 0 {
		if cur := c.qTabs.CurrentTab(); cur != nil {
			c.syncActiveTab(cur)
		} else {
			c.qTabs.SelectIndex(0)
		}
	}
	c.focusEditor()
}

// syncActiveTab re-points c.queryEntry at the selected tab's editor; the
// previous editor's pending candidates are dropped with the shared bar.
func (c *Client) syncActiveTab(item *container.TabItem) {
	for _, t := range c.qItems {
		if t.item == item {
			if old := c.queryEntry; old != nil && old != t.entry {
				old.suggest = nil
				old.sugSel = 0
			}
			c.queryEntry = t.entry
			break
		}
	}
	if c.sugBar != nil {
		c.sugBar.Hide()
		c.sugBar.Refresh()
	}
}

// onQueryTextChanged recomputes the completion candidates after each edit
// and keeps the tab's gutter in step.
func (c *Client) onQueryTextChanged() {
	s := c.queryEntry
	c.syncTabMetrics(s)
	w := s.currentWord()
	if w.text == "" {
		s.setSuggestions(nil)
		return
	}
	s.setSuggestions(matchSuggestions(c.suggestionPool(), w.text))
}

// syncTabMetrics refreshes a tab's line-number gutter and keeps the caret in
// view. The editor's MinSize grows with its text (see (*suggestEntry).MinSize),
// so the editor and the gutter fill the tab's scroller and scroll together.
func (c *Client) syncTabMetrics(e *suggestEntry) {
	var tab *queryTab
	for _, t := range c.qItems {
		if t.entry == e {
			tab = t
			break
		}
	}
	if tab == nil || tab.gutter == nil {
		return
	}
	lines := strings.Count(e.Text, "\n") + 1
	var sb strings.Builder
	for i := 1; i <= lines; i++ {
		if i > 1 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "%d", i)
	}
	tab.gutter.SetText(sb.String())
	e.Refresh() // the layout now sees the editor's grown MinSize

	// Keep the caret's line inside the visible band.
	if tab.scroller != nil && tab.scroller.Size().Height > 0 {
		editorMetrics()
		lineH := editorLineH
		caretTop := float32(e.CursorRow) * lineH
		offset := tab.scroller.Offset.Y
		viewH := tab.scroller.Size().Height
		switch {
		case caretTop < offset:
			tab.scroller.ScrollToOffset(fyne.NewPos(0, caretTop))
		case caretTop+lineH > offset+viewH:
			tab.scroller.ScrollToOffset(fyne.NewPos(0, caretTop+lineH-viewH))
		}
	}
}

// suggestionPool gathers completion candidates. It must never touch the
// server on the UI thread: table names come from a memo warmed by a
// background load, kicked off here on a miss, so typing can never freeze the
// window waiting for a catalog query.
func (c *Client) suggestionPool() []string {
	pool := make([]string, 0, len(sqlKeywords)+len(c.dbList)+len(c.gridCols)+16)
	pool = append(pool, sqlKeywords...)
	pool = append(pool, c.dbList...)
	// The last result's columns are prime candidates for follow-up queries
	// (WHERE <col> ..., ORDER BY <col> ...).
	pool = append(pool, c.gridCols...)

	if c.currentID != "" {
		if _, err := c.store.OpenConn(c.currentID); err == nil {
			dbName := c.resolveDB()
			key := c.currentID + "\x00" + dbName
			c.mu.Lock()
			tables, ok := c.sugTables[key]
			loading := c.sugLoading[key]
			if !ok && !loading {
				c.sugLoading[key] = true
			}
			c.mu.Unlock()
			if !ok && !loading {
				go c.loadSuggestTables(c.currentID, dbName, key)
			}
			pool = append(pool, tables...)
		}
	}
	return pool
}

// loadSuggestTables reads the table names for SQL completion off the UI
// thread; database/sql and the guarded maps are safe for this, and nothing
// here touches a widget. A failed load is NOT memoized, so the next
// keystroke retries instead of leaving completion empty for the session.
func (c *Client) loadSuggestTables(connID, db, key string) {
	var tables []string
	infos, err := c.store.Tables(connID, db, "")
	if err == nil {
		for _, ti := range infos {
			tables = append(tables, ti.Name)
		}
	}
	c.mu.Lock()
	if err == nil {
		c.sugTables[key] = tables
	}
	delete(c.sugLoading, key)
	c.mu.Unlock()
}

// renderSuggestions redraws the candidate bar under the SQL editor.
func (c *Client) renderSuggestions() {
	if c.sugBar == nil {
		return
	}
	s := c.queryEntry
	c.sugBar.Objects = c.sugBar.Objects[:0]
	for i, cand := range s.suggest {
		i, cand := i, cand
		b := widget.NewButton(cand, func() {
			s.sugSel = i
			s.acceptSuggestion()
		})
		if i == s.sugSel {
			b.Importance = widget.HighImportance
		}
		c.sugBar.Objects = append(c.sugBar.Objects, b)
	}
	if len(s.suggest) == 0 {
		c.sugBar.Hide()
	} else {
		c.sugBar.Show()
	}
	c.sugBar.Refresh()
}
