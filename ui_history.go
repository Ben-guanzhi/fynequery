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
)

// buildHistoryPanel assembles the sidebar bottom: recent statements with a
// compact header, capped to a fixed height like TablePro's history drawer.
func (c *Client) buildHistoryPanel() fyne.CanvasObject {
	c.histFilterEntry = widget.NewEntry()
	c.histFilterEntry.PlaceHolder = "过滤"
	c.histFilterEntry.OnChanged = func(text string) {
		c.histFilter = strings.ToLower(strings.TrimSpace(text))
		c.rebuildHistoryView()
	}

	c.historyList = widget.NewList(
		func() int { return len(c.historyView) },
		func() fyne.CanvasObject {
			return container.NewVBox(
				widget.NewLabel("placeholder"),
				widget.NewLabel(""),
			)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(c.historyView) {
				return
			}
			e := c.historyView[id]
			box, ok := obj.(*fyne.Container)
			if !ok || len(box.Objects) < 2 {
				return
			}
			title, _ := box.Objects[0].(*widget.Label)
			sub, _ := box.Objects[1].(*widget.Label)

			sqlLine := strings.Join(strings.Fields(e.SQL), " ")
			if r := []rune(sqlLine); len(r) > 60 {
				sqlLine = string(r[:60]) + " …"
			}
			title.SetText(sqlLine)
			title.TextStyle = fyne.TextStyle{Monospace: true}

			when := historyTimeLabel(e.Time)
			meta := when
			if e.Error != "" {
				meta += "  · 错误"
				sub.Importance = widget.DangerImportance
			} else if e.RowCount > 0 {
				meta += fmt.Sprintf("  · %d 行", e.RowCount)
				sub.Importance = widget.LowImportance
			} else {
				meta += fmt.Sprintf("  · 影响 %d 行", e.Affected)
				sub.Importance = widget.LowImportance
			}
			if e.Database != "" {
				meta += "  · " + e.Database
			}
			sub.SetText(meta)
		},
	)
	c.historyList.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(c.historyView) {
			return
		}
		e := c.historyView[id]
		c.queryEntry.setTextSilently(e.SQL)
		if e.Database != "" {
			c.currentDB = e.Database
		}
		c.setStatus("已加载历史语句")
		c.focusEditor()
	}

	rerunBtn := widget.NewButtonWithIcon("", theme.MediaPlayIcon(), func() {
		if c.queryEntry.Text != "" {
			c.runCurrent()
		}
	})
	rerunBtn.Importance = widget.LowImportance
	clearBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
		dialog.ShowConfirm("清空历史", "确定清空全部查询历史吗？", func(ok bool) {
			if !ok {
				return
			}
			c.clearHistory()
			c.refreshHistory()
			c.setStatus("历史已清空")
		}, c.win)
	})
	clearBtn.Importance = widget.LowImportance

	header := container.NewBorder(nil, nil, widget.NewLabel("历史"),
		container.NewHBox(rerunBtn, clearBtn), c.histFilterEntry)
	// The sidebar's bottom slot takes this panel's min height; a deterministic
	// fixed-height layout keeps the band stable no matter what the list reports.
	historyPanel := container.NewBorder(header, nil, nil, nil, c.historyList)
	return container.New(&fixedHeightLayout{height: 150}, historyPanel)
}

// rebuildHistoryView recomputes the filtered newest-first view the history
// list renders. Called whenever the log or the filter changes.
func (c *Client) rebuildHistoryView() {
	c.mu.Lock()
	view := make([]HistoryEntry, 0, len(c.history))
	for i := len(c.history) - 1; i >= 0; i-- {
		e := c.history[i]
		if c.histFilter == "" ||
			strings.Contains(strings.ToLower(e.SQL), c.histFilter) ||
			strings.Contains(strings.ToLower(e.Table), c.histFilter) ||
			strings.Contains(strings.ToLower(e.Database), c.histFilter) {
			view = append(view, e)
		}
	}
	c.mu.Unlock()
	c.historyView = view
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
	if c.historyList == nil {
		return
	}
	c.rebuildHistoryView()
	c.historyList.Refresh()
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
