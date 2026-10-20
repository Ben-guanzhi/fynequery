package main

// The SQL editor orchestration: query tabs sharing one result grid, the
// line-number gutter, the completion candidate pool and bar. The suggestEntry
// control itself (word detection, key interception, splicing) is in
// ui_suggest.go.

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// newQueryEntry builds one tab's SQL editor; completion only reacts while its
// own tab is the active one. SQL renders monospace so the line-number gutter
// aligns with the editor's lines exactly.
func (c *Client) newQueryEntry() *suggestEntry {
	e := newSuggestEntry()
	e.SetPlaceHolder("在此输入 SQL …… 按 Ctrl+Enter 执行")
	e.TextStyle = fyne.TextStyle{Monospace: true}
	e.onSugChanged = c.renderSuggestions
	e.onLines = func() { c.syncTabMetrics(e) }
	e.onShortcut = c.entryShortcut
	e.OnChanged = func(string) {
		if c.tabs.entry == e {
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
	c.tabs.seq++
	e := c.newQueryEntry()
	gutter := widget.NewMultiLineEntry()
	gutter.TextStyle = fyne.TextStyle{Monospace: true}
	gutter.Wrapping = fyne.TextWrapOff
	gutter.Disable()
	row := container.NewBorder(nil, nil, gutter, nil, e)
	scroller := container.NewVScroll(row)
	item := container.NewTabItem(fmt.Sprintf("查询 %d", c.tabs.seq), scroller)
	c.tabs.items = append(c.tabs.items, &queryTab{item: item, entry: e, gutter: gutter, scroller: scroller, baseTitle: item.Text})
	c.tabs.bar.Append(item)
	c.tabs.bar.Select(item)
	// fyne 2.8 auto-selects a first tab without firing OnSelected; make the
	// active-editor alias explicit either way.
	c.syncActiveTab(item)
	c.syncTabMetrics(e)
}

// selectTab switches to the tab at index i. An index past the last tab does
// nothing: Ctrl+5 on three tabs should not wrap around and steal a tab the
// user did not ask for.
func (c *Client) selectTab(i int) {
	if i < 0 || i >= len(c.tabs.items) {
		return
	}
	// SelectIndex fires OnSelected, which is what re-points c.tabs.entry.
	c.tabs.bar.SelectIndex(i)
	c.focusEditor()
}

// newTableTab opens the "新建表" draft: a fresh tab holding the engine's
// starter CREATE TABLE, renamed from 查询 N so the strip says what it is, and
// the session switched to the branch's database because the statement runs
// where the session points. SetText goes through OnChanged, so the tab is
// marked dirty like any hand-typed draft.
func (c *Client) newTableTab(nd *nodeData) {
	id := c.selectedOrLive()
	conn, err := c.store.OpenConn(id)
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	stmt, err := db.CreateTableTemplate(conn.Config.Type, nd.schema)
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	if nd.db != "" && nd.db != c.currentDB {
		c.currentDB = nd.db
		c.dbSelRefresh()
	}
	c.addQueryTab()
	tab := c.tabByItem(c.tabs.bar.CurrentTab())
	if tab == nil {
		return
	}
	tab.baseTitle = "新建表"
	tab.item.Text = tab.baseTitle
	c.tabs.entry.SetText(stmt)
	c.setStatus("新建表草案已打开，改完名字和字段再执行")
}

// restoreQueryTabs refills the freshly built tab strip from the persisted
// drafts: the first draft lands in the tab addQueryTab already created, the
// rest append new tabs. setTextSilently keeps the restore from looking like
// typing (no completion pass, no dirty mark); the dirty flag and its ● title
// are replayed from the draft instead.
func (c *Client) restoreQueryTabs() {
	for i, d := range c.uiSet.Tabs {
		if i >= len(c.tabs.items) {
			c.addQueryTab()
		}
		t := c.tabs.items[i]
		t.entry.setTextSilently(d.Content)
		t.dirty = d.Dirty
		if d.Dirty {
			t.item.Text = t.baseTitle + " ●"
		}
	}
	if len(c.uiSet.Tabs) > 0 {
		c.tabs.bar.Refresh()
	}
}

// markTabDirty flags a tab whose text changed since it last ran.
func (c *Client) markTabDirty(e *suggestEntry) {
	for _, t := range c.tabs.items {
		if t.entry == e && !t.dirty {
			t.dirty = true
			t.item.Text = t.baseTitle + " ●"
			c.tabs.bar.Refresh()
			return
		}
	}
}

// clearTabDirty clears the modified marker once the tab's text was executed.
func (c *Client) clearTabDirty(e *suggestEntry) {
	for _, t := range c.tabs.items {
		if t.entry == e && t.dirty {
			t.dirty = false
			t.item.Text = t.baseTitle
			c.tabs.bar.Refresh()
			return
		}
	}
}

// tabByItem finds the bookkeeping record of a tab-strip item.
func (c *Client) tabByItem(item *container.TabItem) *queryTab {
	for _, t := range c.tabs.items {
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
	if len(c.tabs.items) <= 1 {
		return
	}
	item := c.tabs.bar.CurrentTab()
	tab := c.tabByItem(item)
	if tab != nil && tab.dirty {
		dialog.ShowConfirm("确认关闭标签",
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
	for i, t := range c.tabs.items {
		if t.item == item {
			c.tabs.items = append(c.tabs.items[:i], c.tabs.items[i+1:]...)
			break
		}
	}
	c.tabs.bar.Remove(item)
	if len(c.tabs.items) > 0 {
		if cur := c.tabs.bar.CurrentTab(); cur != nil {
			c.syncActiveTab(cur)
		} else {
			c.tabs.bar.SelectIndex(0)
		}
	}
	c.focusEditor()
}

// syncActiveTab re-points c.tabs.entry at the selected tab's editor; the
// previous editor's pending candidates are dropped with the shared bar.
func (c *Client) syncActiveTab(item *container.TabItem) {
	for _, t := range c.tabs.items {
		if t.item == item {
			if old := c.tabs.entry; old != nil && old != t.entry {
				old.suggest = nil
				old.sugSel = 0
			}
			c.tabs.entry = t.entry
			break
		}
	}
	if c.tabs.sug != nil {
		c.tabs.sug.Hide()
		c.tabs.sug.Refresh()
	}
}

// onQueryTextChanged recomputes the completion candidates after each edit
// and keeps the tab's gutter in step.
func (c *Client) onQueryTextChanged() {
	s := c.tabs.entry
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
	for _, t := range c.tabs.items {
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
	pool := make([]string, 0, len(sqlKeywords)+len(c.dbs.list)+len(c.grid.cols)+16)
	pool = append(pool, sqlKeywords...)
	pool = append(pool, c.dbs.list...)
	// The last result's columns are prime candidates for follow-up queries
	// (WHERE <col> ..., ORDER BY <col> ...).
	pool = append(pool, c.grid.cols...)

	if c.currentID != "" {
		if _, err := c.store.OpenConn(c.currentID); err == nil {
			dbName := c.resolveDB()
			key := c.currentID + "\x00" + dbName
			c.mu.Lock()
			tables, ok := c.cache.tables[key]
			loading := c.cache.loading[key]
			if !ok && !loading {
				c.cache.loading[key] = true
			}
			objects, okObjects := c.cache.objects[key]
			loadingObjects := c.cache.loadingObjects[key]
			if !okObjects && !loadingObjects {
				c.cache.loadingObjects[key] = true
			}
			c.mu.Unlock()
			if !ok && !loading {
				go c.loadSuggestTables(context.Background(), c.currentID, dbName, key)
			}
			if !okObjects && !loadingObjects {
				go c.loadSuggestObjects(context.Background(), c.currentID, dbName, key)
			}
			pool = append(pool, tables...)
			pool = append(pool, objects...)
		}
	}
	return pool
}

// loadSuggestObjects adds the engine's non-table catalog names: PL/SQL
// Developer's Code Assistant list (Oracle packages, types, procedures,
// functions, sequences, synonyms, database links). Tables already come from
// loadSuggestTables. A failed load is NOT memoized, so the next keystroke
// retries instead of leaving completion empty for the session.
func (c *Client) loadSuggestObjects(ctx context.Context, connID, db, key string) {
	var objects []string
	names, err := c.store.CompletionObjects(ctx, connID, db)
	if err == nil {
		objects = names
	}
	c.mu.Lock()
	if err == nil {
		c.cache.objects[key] = objects
	}
	delete(c.cache.loadingObjects, key)
	c.mu.Unlock()
}

// loadSuggestTables reads the table names for SQL completion off the UI
// thread; database/sql and the guarded maps are safe for this, and nothing
// here touches a widget. A failed load is NOT memoized, so the next
// keystroke retries instead of leaving completion empty for the session.
func (c *Client) loadSuggestTables(ctx context.Context, connID, db, key string) {
	var tables []string
	infos, err := c.store.Tables(ctx, connID, db, "")
	if err == nil {
		for _, ti := range infos {
			tables = append(tables, ti.Name)
		}
	}
	c.mu.Lock()
	if err == nil {
		c.cache.tables[key] = tables
	}
	delete(c.cache.loading, key)
	c.mu.Unlock()
}

// renderSuggestions redraws the candidate bar under the SQL editor.
func (c *Client) renderSuggestions() {
	if c.tabs.sug == nil {
		return
	}
	s := c.tabs.entry
	c.tabs.sug.Objects = c.tabs.sug.Objects[:0]
	for i, cand := range s.suggest {
		i, cand := i, cand
		b := widget.NewButton(cand, func() {
			s.sugSel = i
			s.acceptSuggestion()
		})
		if i == s.sugSel {
			b.Importance = widget.HighImportance
		}
		c.tabs.sug.Objects = append(c.tabs.sug.Objects, b)
	}
	if len(s.suggest) == 0 {
		c.tabs.sug.Hide()
	} else {
		c.tabs.sug.Show()
	}
	c.tabs.sug.Refresh()
}
