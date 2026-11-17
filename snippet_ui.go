package main

// 片段库的 Fyne UI：侧栏第四个模式（项目 | 查询 | 历史记录 | 片段）。
// 面板 = 搜索框 + 列表（选中载入编辑器）+ 「存当前编辑器」「删选中」。
// 数据层在 snippet.go。

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// buildSnippetPane 组装片段面板。sid.state 持有选中态与 store 引用。
func (b *sidebarBody) buildSnippetPane() fyne.CanvasObject {
	s := b.s
	// 测试/简化构造可能没接 snippetStore——空面板而不是 panic。
	if s.snippetStore == nil {
		return widget.NewLabel("片段库未初始化")
	}
	s.snippetStore.load()

	search := widget.NewEntry()
	search.PlaceHolder = "搜索片段..."
	search.OnChanged = func(string) { b.snippetList.Refresh() }

	snipList := widget.NewList(
		func() int { return len(b.filteredSnippets(search.Text)) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i int, o fyne.CanvasObject) {
			items := b.filteredSnippets(search.Text)
			if i < 0 || i >= len(items) {
				return
			}
			lbl := o.(*widget.Label)
			lbl.Truncation = fyne.TextTruncateEllipsis
			sn := items[i]
			lbl.SetText(sn.Name + "  ·  " + snippetFirstLine(sn.SQL))
		},
	)
	b.snippetList = snipList
	snipList.OnSelected = func(id widget.ListItemID) {
		items := b.filteredSnippets(search.Text)
		if id < 0 || id >= len(items) {
			return
		}
		snipList.UnselectAll()
		s.snippetSel = items[id].Name
		// 载入：片段 SQL 回编辑器（跨连接复用——不挑当前连接）。
		if s.onSnippetPick != nil {
			s.onSnippetPick(items[id].SQL)
		}
	}

	nameEntry := widget.NewEntry()
	nameEntry.PlaceHolder = "片段名（同名覆盖）"
	saveBtn := widget.NewButton("存当前编辑器", func() {
		name := strings.TrimSpace(nameEntry.Text)
		sql := strings.TrimSpace(s.currentEditorText())
		if name == "" || sql == "" {
			s.setSnippetMsg("片段名与编辑器内容都要有")
			return
		}
		if err := s.snippetStore.put(snippet{Name: name, SQL: sql}); err != nil {
			s.setSnippetMsg(err.Error())
			return
		}
		nameEntry.SetText("")
		s.setSnippetMsg("已保存片段 " + name)
		b.snippetList.Refresh()
	})
	delBtn := widget.NewButton("删选中", func() {
		if s.snippetSel == "" {
			s.setSnippetMsg("先在列表里选中一个片段")
			return
		}
		if err := s.snippetStore.del(s.snippetSel); err != nil {
			s.setSnippetMsg(err.Error())
			return
		}
		s.setSnippetMsg("已删除 " + s.snippetSel)
		s.snippetSel = ""
		b.snippetList.Refresh()
	})

	top := container.NewVBox(
		container.NewBorder(nil, nil, nil, container.NewHBox(saveBtn, widget.NewButton("删选中", delBtn.OnTapped)), nameEntry),
		search,
	)
	return container.NewBorder(top, nil, nil, nil, snipList)
}

// filteredSnippets 按搜索词过滤（名字或内容前缀命中），名字排序。
func (b *sidebarBody) filteredSnippets(term string) []snippet {
	term = strings.ToLower(strings.TrimSpace(term))
	all := b.s.snippetStore.list()
	if term == "" {
		return all
	}
	out := make([]snippet, 0, len(all))
	for _, sn := range all {
		if strings.Contains(strings.ToLower(sn.Name), term) ||
			strings.Contains(strings.ToLower(sn.SQL), term) {
			out = append(out, sn)
		}
	}
	return out
}

// setSnippetMsg 在侧栏状态行给一行反馈（片段操作的轻量确认）。
func (s *sidebar) setSnippetMsg(msg string) {
	if s.body == nil || s.body.status == nil {
		return
	}
	s.body.status.SetText(msg)
}
