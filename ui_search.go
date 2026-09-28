package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// showSearchDialog opens the quick-open box: type a fragment, press Enter,
// pick a hit to jump to it in the navigator with a SELECT filled in.
func (c *Client) showSearchDialog() {
	c.searchResults = nil

	entry := widget.NewEntry()
	entry.SetPlaceHolder("表名片段，回车搜索（跨当前所有已连接的库）")

	hint := widget.NewLabel("输入片段后按回车")
	hint.TextStyle = fyne.TextStyle{Italic: true}

	// One live connection: the column would repeat the same name everywhere.
	showConn := false
	c.mu.Lock()
	showConn = c.store.LiveCount() > 1
	c.mu.Unlock()

	var d *dialog.CustomDialog
	results := widget.NewList(
		func() int { return len(c.searchResults) },
		func() fyne.CanvasObject { return widget.NewLabel("placeholder") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			label, ok := obj.(*widget.Label)
			if !ok || id < 0 || id >= len(c.searchResults) {
				return
			}
			label.SetText(c.searchResultText(c.searchResults[id], showConn))
			label.Truncation = fyne.TextTruncateEllipsis
		},
	)
	results.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(c.searchResults) {
			return
		}
		match := c.searchResults[id]
		d.Hide()
		results.UnselectAll()
		c.openObjectMatch(match)
	}

	runSearch := func() {
		hits, err := c.store.SearchObjects(entry.Text, 0)
		if err != nil {
			c.searchResults = nil
			hint.SetText("搜索失败：" + err.Error())
			results.Refresh()
			return
		}
		c.searchResults = hits
		if len(hits) == 0 {
			hint.SetText("没有匹配的对象")
		} else {
			extra := ""
			if len(hits) >= db.SearchDefaultLimit {
				extra = "（已达上限，输入更长的片段可缩小范围）"
			}
			hint.SetText(fmt.Sprintf("找到 %d 个对象%s", len(hits), extra))
		}
		results.Refresh()
	}
	entry.OnSubmitted = func(string) { runSearch() }

	buttons := container.NewHBox(
		widget.NewButtonWithIcon("搜索", theme.SearchIcon(), runSearch),
		layout.NewSpacer(),
	)
	content := container.NewBorder(
		container.NewVBox(entry, hint), buttons, nil, nil, results,
	)
	d = dialog.NewCustom("快速打开（Ctrl+P）", "关闭", content, c.win)
	d.Resize(fyne.NewSize(520, 420))
	d.Show()
}

// searchResultText renders one hit: where it lives and what it is.
func (c *Client) searchResultText(m db.ObjectMatch, showConn bool) string {
	name := m.Table
	if m.Schema != "" {
		name = m.Schema + "." + m.Table
	}
	text := fmt.Sprintf("%s · %s [%s]", m.Database, name, m.Type)
	if showConn {
		if cfg, ok := c.store.FindConnection(m.ConnID); ok {
			text = cfg.Name + " · " + text
		}
	}
	return text
}

// openObjectMatch jumps to a search hit: switches the connection and database,
// expands the navigator down to the table, selects it and fills the editor.
// Catalog quirks degrade gracefully: if the node cannot be found the editor
// still gets a composed SELECT.
func (c *Client) openObjectMatch(m db.ObjectMatch) {
	c.currentID = m.ConnID
	c.currentDB = m.Database
	c.refreshSchema()

	tableUID := ""
	if _, ok := c.treeNodes["db:"+m.Database]; ok {
		c.toggleSchemaNode("db:" + m.Database)
		dbNode := c.treeNodes["db:"+m.Database]
		for _, child := range dbNode.children {
			childNode := c.treeNodes[child]
			if childNode == nil {
				continue
			}
			if childNode.kind == "schema" {
				if m.Schema != "" && !strings.EqualFold(childNode.schema, m.Schema) {
					continue
				}
				c.toggleSchemaNode(child)
				for _, t := range childNode.children {
					if tNode := c.treeNodes[t]; tNode != nil && tNode.name == m.Table {
						tableUID = t
						break
					}
				}
			} else if childNode.name == m.Table {
				tableUID = child
			}
			if tableUID != "" {
				break
			}
		}
	}

	if tableUID != "" {
		c.onSchemaTableTapped(tableUID)
		return
	}
	// The tree could not surface the node (load failure, renamed catalog);
	// still give the editor a working statement.
	c.queryEntry.setTextSilently(c.selectSQL(&nodeData{db: m.Database, schema: m.Schema, name: m.Table}))
	c.dbSelRefresh()
	c.setStatus("已在编辑器打开 " + m.Table)
}
