package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// Favorites: the half of TablePlus's 历史 panel this client was missing. A log
// answers "what did I run"; a favorite answers "which of those do I always
// need". The list itself is the shared sidebar list (ui_history.go) - this file
// is what fills its rows and the three things the user does with one: save it,
// load it, drop it.

// favoriteRow renders one saved statement: the statement on one line, and
// under it when it was saved, its label if it has one, and its database.
func favoriteRow(e db.FavoriteEntry) sidebarEntry {
	title := oneLine(e.SQL, 60)
	if title == "" {
		title = e.Name
	}
	meta := historyTimeLabel(e.Time)
	if e.Name != "" {
		meta += "  · " + e.Name
	}
	if e.Database != "" {
		meta += "  · " + e.Database
	}
	return sidebarEntry{ID: e.ID, Title: title, Meta: meta}
}

// favoritesTab is the 收藏 tab: the list with its filter and the two buttons
// that shrink it. There is no 载入 button because clicking a row already loads
// it, and a second way in would only raise the question of which one is the
// real one.
func (c *Client) favoritesTab() fyne.CanvasObject {
	delInner := widget.NewButtonWithIcon("", theme.DeleteIcon(), c.deleteSelectedFavorite)
	delInner.Importance = widget.LowImportance
	delBtn := attachTooltip(delInner, "删除选中项")
	clearInner := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
		if len(c.store.Favorites()) == 0 {
			c.setStatusWarn("收藏列表已经是空的")
			return
		}
		dialog.ShowConfirm("确认清空收藏", "确定清空全部收藏吗？", func(ok bool) {
			if !ok {
				return
			}
			n := c.store.ClearFavorites()
			c.refreshFavorites()
			c.setStatus(fmt.Sprintf("已清空 %d 条收藏", n))
		}, c.win)
	})
	clearInner.Importance = widget.LowImportance
	clearBtn := attachTooltip(clearInner, "清空全部收藏")

	header := container.NewBorder(nil, nil, nil,
		container.NewHBox(delBtn, clearBtn), c.fav.filterEntry)
	return container.NewBorder(header, nil, nil, nil, c.fav.list)
}

// favoriteCurrentStatement saves what the editor is showing (Ctrl/Cmd+B). It
// is the one way in that does not go through the list, which is the point: the
// statement worth keeping is usually the one being worked on right now.
func (c *Client) favoriteCurrentStatement() {
	if c.tabs.entry == nil {
		return
	}
	sql := strings.TrimSpace(c.tabs.entry.Text)
	if sql == "" {
		c.setStatusWarn("编辑器是空的，没有可收藏的语句")
		return
	}
	// The connection and database ride along so the panel can say where the
	// statement belongs; they are not needed to load it back.
	if _, added := c.store.SaveFavorite(c.selectedOrLive(), c.currentDB, sql); !added {
		c.setStatusWarn("这条语句已经收藏过了")
		return
	}
	c.refreshFavorites()
	c.setStatus("已收藏当前语句")
}

// deleteSelectedFavorite removes the one row currently selected and
// re-renders. Like history it addresses the entry by its stable ID, so the
// filtered view and the file stay in step.
func (c *Client) deleteSelectedFavorite() {
	if c.fav.selected < 0 || c.fav.selected >= len(c.fav.view) {
		c.setStatusWarn("先选中一条收藏再删除")
		return
	}
	id := c.fav.view[c.fav.selected].ID
	if !c.store.DeleteFavorite(id) {
		c.setStatusWarn("该条收藏已不在列表中")
		return
	}
	c.refreshFavorites()
	c.setStatus("已删除一条收藏")
}

// loadFavorite puts a saved statement back in the editor and points the
// session at the database it was saved against.
func (c *Client) loadFavorite(e db.FavoriteEntry) {
	if c.tabs.entry == nil {
		return
	}
	c.tabs.entry.setTextSilently(e.SQL)
	if e.Database != "" {
		c.currentDB = e.Database
	}
	c.setStatus("已载入收藏语句")
	c.focusEditor()
}

// refreshFavorites re-renders the favorites list from the store.
func (c *Client) refreshFavorites() {
	if c.fav == nil {
		return
	}
	c.fav.setSource(c.store.Favorites())
}
