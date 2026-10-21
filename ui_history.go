package main

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// The sidebar's bottom band. 历史 is what you ran; 收藏 is what you meant to
// run again. They are the same widget - a filter box over a list of one-line
// statements - so they are built from one generic list and differ only in what
// fills the rows and which buttons the header carries.

// sidebarEntry is one row of either list: the statement squeezed onto one
// line, the line under it, and the id the header's buttons act on.
type sidebarEntry struct {
	ID     int64
	Title  string
	Meta   string
	Danger bool
}

// entryList is one of those lists. T is the source type - a history or a
// favorite entry - so neither tab has to flatten its entries into rows before
// the panel sees them: row decides what a row says and match decides what the
// filter keeps, and everything below is written once.
type entryList[T any] struct {
	list        *widget.List
	filterEntry *keyedEntry
	filter      string
	source      []T
	// view is the filtered newest-first rows the list renders; selected is
	// the row the header's buttons act on, -1 for none.
	view     []T
	selected int
	row      func(T) sidebarEntry
	match    func(T, string) bool
}

// newEntryList builds the filter box and the list. onSelect is what clicking a
// row does; both tabs load the statement into the editor, which is also why a
// row is not deleted by clicking it.
func newEntryList[T any](c *Client, row func(T) sidebarEntry, match func(T, string) bool,
	onSelect func(T)) *entryList[T] {
	e := &entryList[T]{selected: -1, row: row, match: match}
	e.filterEntry = newKeyedEntry()
	e.filterEntry.SetPlaceHolder("过滤")
	e.filterEntry.onShortcut = c.entryShortcut
	e.filterEntry.OnChanged = func(text string) {
		e.filter = strings.ToLower(strings.TrimSpace(text))
		e.refilter()
		e.list.Refresh()
	}
	e.list = widget.NewList(
		func() int { return len(e.view) },
		func() fyne.CanvasObject {
			return container.NewVBox(widget.NewLabel("placeholder"), widget.NewLabel(""))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(e.view) {
				return
			}
			box, ok := obj.(*fyne.Container)
			if !ok || len(box.Objects) < 2 {
				return
			}
			title, _ := box.Objects[0].(*widget.Label)
			sub, _ := box.Objects[1].(*widget.Label)
			if title == nil || sub == nil {
				return
			}
			entry := e.row(e.view[id])
			title.SetText(entry.Title)
			title.TextStyle = fyne.TextStyle{Monospace: true}
			sub.SetText(entry.Meta)
			sub.Importance = widget.LowImportance
			if entry.Danger {
				sub.Importance = widget.DangerImportance
			}
		},
	)
	e.list.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(e.view) {
			return
		}
		e.selected = id
		if onSelect != nil {
			onSelect(e.view[id])
		}
	}
	return e
}

// refilter rebuilds the newest-first view the list renders. A rebuilt view
// renumbers its rows, so the recorded selection goes with it rather than
// pointing at whatever now sits in that row.
func (e *entryList[T]) refilter() {
	e.selected = -1
	view := make([]T, 0, len(e.source))
	for i := len(e.source) - 1; i >= 0; i-- {
		item := e.source[i]
		if e.filter == "" || e.match(item, e.filter) {
			view = append(view, item)
		}
	}
	e.view = view
}

// setSource hands the list a fresh source and re-filters it.
func (e *entryList[T]) setSource(source []T) {
	e.source = source
	e.refilter()
	if e.list != nil {
		e.list.Refresh()
	}
}

// historyRow renders one log entry: the statement on one line, and under it
// when it ran and what it did.
func historyRow(e db.HistoryEntry) sidebarEntry {
	meta := historyTimeLabel(e.Time)
	danger := e.Error != ""
	switch {
	case danger:
		meta += "  · 错误"
	case e.RowCount > 0:
		meta += fmt.Sprintf("  · %d 行", e.RowCount)
	default:
		meta += fmt.Sprintf("  · 影响 %d 行", e.Affected)
	}
	if e.Database != "" {
		meta += "  · " + e.Database
	}
	return sidebarEntry{ID: e.ID, Title: oneLine(e.SQL, 60), Meta: meta, Danger: danger}
}

// oneLine squeezes a statement into one line and cuts it at limit runes so a
// formatted script cannot stretch a row; the cut happens on runes so a
// multibyte statement cannot end mid-character.
func oneLine(sql string, limit int) string {
	line := strings.Join(strings.Fields(sql), " ")
	if r := []rune(line); len(r) > limit {
		return string(r[:limit]) + " …"
	}
	return line
}

// buildHistoryPanel assembles the sidebar's bottom band: the 历史 and 收藏
// tabs, capped to a fixed height like TablePro's history drawer.
func (c *Client) buildHistoryPanel() fyne.CanvasObject {
	c.buildEntryLists()
	tabs := container.NewAppTabs(
		container.NewTabItem("历史", c.historyTab()),
		container.NewTabItem("收藏", c.favoritesTab()),
	)
	// The band is a fixed height so the navigator above it keeps its size; the
	// tab strip takes ~35px of it, which the list would otherwise pay for out
	// of its three visible rows.
	return container.New(&fixedHeightLayout{height: 190}, tabs)
}

// buildEntryLists creates the two sidebar lists. It sits apart from
// buildHistoryPanel so a test can hold the lists - and ask them what they would
// render - without assembling the whole tab strip.
func (c *Client) buildEntryLists() {
	c.hist = newEntryList(c, historyRow,
		func(e db.HistoryEntry, f string) bool { return e.MatchesFilter(f) },
		c.loadHistoryEntry)
	c.fav = newEntryList(c, favoriteRow,
		func(e db.FavoriteEntry, f string) bool { return e.MatchesFilter(f) },
		c.loadFavorite)
}

// historyTab is the 历史 tab: the log with its filter, plus the three buttons
// that act on it.
func (c *Client) historyTab() fyne.CanvasObject {
	rerunInner := widget.NewButtonWithIcon("", theme.MediaPlayIcon(), func() {
		if c.tabs.entry != nil && c.tabs.entry.Text != "" {
			c.runAllStatements()
		}
	})
	rerunInner.Importance = widget.LowImportance
	rerunBtn := attachTooltip(rerunInner, "重跑当前编辑器中的语句")
	delInner := widget.NewButtonWithIcon("", theme.DeleteIcon(), c.deleteSelectedHistory)
	delInner.Importance = widget.LowImportance
	delBtn := attachTooltip(delInner, "删除选中项")
	clearInner := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
		dialog.ShowConfirm("确认清空历史", "确定清空全部查询历史吗？", func(ok bool) {
			if !ok {
				return
			}
			c.store.ClearHistory()
			c.refreshHistory()
			c.setStatus("历史已清空")
		}, c.win)
	})
	clearInner.Importance = widget.LowImportance
	clearBtn := attachTooltip(clearInner, "清空全部历史")

	header := container.NewBorder(nil, nil, nil,
		container.NewHBox(rerunBtn, delBtn, clearBtn), c.hist.filterEntry)
	return container.NewBorder(header, nil, nil, nil, c.hist.list)
}

// deleteSelectedHistory removes the one history row currently selected from
// the log and re-renders. Entries are addressed by their stable ID, so the
// filtered view the user is looking at and the on-disk log stay in step.
func (c *Client) deleteSelectedHistory() {
	if c.hist.selected < 0 || c.hist.selected >= len(c.hist.view) {
		c.setStatusWarn("先选中一条历史再删除")
		return
	}
	id := c.hist.view[c.hist.selected].ID
	if !c.store.DeleteHistory(id) {
		c.setStatusWarn("该条历史已不在日志中")
		return
	}
	c.refreshHistory()
	c.setStatus("已删除一条历史")
}

// loadHistoryEntry puts a log entry back in the editor, and points the session
// at the database it ran against.
func (c *Client) loadHistoryEntry(e db.HistoryEntry) {
	if c.tabs.entry == nil {
		return
	}
	c.tabs.entry.setTextSilently(e.SQL)
	if e.Database != "" {
		c.currentDB = e.Database
	}
	c.setStatus("已加载历史语句")
	c.focusEditor()
}

// fixedHeightLayout lays its single child out at the full width but an exact
// height, reporting that same height as the minimum.
type fixedHeightLayout struct {
	height float32
}

// Layout implements fyne.Layout.
func (f *fixedHeightLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Resize(fyne.NewSize(size.Width, f.height))
		o.Move(fyne.NewPos(0, 0))
	}
}

// MinSize implements fyne.Layout.
func (f *fixedHeightLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, f.height)
}

// refreshHistory re-renders the history list.
func (c *Client) refreshHistory() {
	if c.hist == nil {
		return
	}
	c.rebuildHistoryView()
}

// rebuildHistoryView recomputes the filtered newest-first view the history
// list renders, called whenever the log or the filter changes.
func (c *Client) rebuildHistoryView() {
	c.mu.Lock()
	c.hist.setSource(c.store.History())
	c.mu.Unlock()
}

// historyTimeLabel renders an entry timestamp: time only for today, with the
// date prefixed for older entries so a long history stays unambiguous.
func historyTimeLabel(unixMillis int64) string {
	t := time.UnixMilli(unixMillis)
	if t.Format("2006-01-02") == time.Now().Format("2006-01-02") {
		return t.Format("15:04:05")
	}
	return t.Format("01-02 15:04")
}
