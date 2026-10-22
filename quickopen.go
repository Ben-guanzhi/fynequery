package main

// Quick-open palette (TablePlus OpenAnything): a bounded, fuzzy search over
// the saved connections, the current database's tables/views, and the query
// history. Picking a hit jumps straight to it — open the table, switch the
// connection, or drop the SQL into the active editor.

import (
	"fmt"
	"fynequery/icons"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// objectMatch is one quick-open candidate.
type objectMatch struct {
	Kind     string // "connection" | "table" | "query" | "action"
	ConnName string // connection name (Kind == "connection")
	Database string // owning database (Kind == "table")
	Table    string // table name (Kind == "table")
	SQL      string // statement (Kind == "query")
	Action   string // application command (Kind == "action")
	Label    string // primary searchable text
}

// quickOpenLimit caps how many hits the palette shows (TablePlus OpenAnything
// caps at ~20; we allow a little more for history + connections).
const quickOpenLimit = 50

// showQuickOpen opens the Ctrl+K palette.
func (ws *workspace) showQuickOpen() {
	if ws.window == nil {
		return
	}
	entry := widget.NewEntry()
	entry.SetPlaceHolder("表名 / 历史 SQL / 连接 / 功能…（^前缀 $后缀精确匹配）")
	hint := widget.NewLabel("输入片段后按回车")
	hint.TextStyle = fyne.TextStyle{Italic: true}

	var results []objectMatch
	list := widget.NewList(
		func() int { return len(results) },
		func() fyne.CanvasObject { return widget.NewLabel("placeholder") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			label, ok := obj.(*widget.Label)
			if !ok || id < 0 || id >= len(results) {
				return
			}
			label.SetText(ws.quickOpenText(results[id]))
			label.Truncation = fyne.TextTruncateEllipsis
		},
	)
	var d *dialog.CustomDialog
	list.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(results) {
			return
		}
		d.Hide()
		list.UnselectAll()
		ws.jumpToMatch(results[id])
	}

	runSearch := func() {
		results = ws.quickOpenSearch(entry.Text)
		if len(results) == 0 {
			hint.SetText("没有匹配的对象")
		} else {
			extra := ""
			if len(results) >= quickOpenLimit {
				extra = "（已达上限，输入更长的片段可缩小范围）"
			}
			hint.SetText(fmt.Sprintf("找到 %d 个对象%s", len(results), extra))
		}
		list.Refresh()
	}
	entry.OnSubmitted = func(string) { runSearch() }

	buttons := container.NewHBox(
		widget.NewButtonWithIcon("搜索", icons.Search, runSearch),
		layout.NewSpacer(),
	)
	content := container.NewBorder(
		container.NewVBox(entry, hint), buttons, nil, nil, list,
	)
	d = dialog.NewCustom("快速打开（Ctrl+K）", "关闭", content, ws.window)
	d.Resize(fyne.NewSize(560, 440))
	d.Show()
}

// quickOpenCandidates gathers every jumpable object from local state, plus
// the application commands (TablePlus OpenAnything lists app features too).
func (ws *workspace) quickOpenCandidates() []objectMatch {
	var out []objectMatch
	if conns, err := loadConnections(); err == nil {
		for _, c := range conns {
			out = append(out, objectMatch{Kind: "connection", ConnName: c.Name, Label: c.Name})
		}
	}
	if ws.sess != nil && ws.currentDB != "" {
		if objs, err := ws.sess.Tables(); err == nil {
			for _, o := range objs {
				out = append(out, objectMatch{Kind: "table", Database: ws.currentDB, Table: o.Name, Label: o.Name})
			}
		}
	}
	if ws.side != nil {
		for _, h := range ws.side.historyItems() {
			out = append(out, objectMatch{Kind: "query", SQL: h, Label: h})
		}
	}
	// Application commands (TablePlus OpenAnything feature list).
	out = append(out,
		objectMatch{Kind: "action", Action: "new-query", Label: "新建查询 (New Query)"},
		objectMatch{Kind: "action", Action: "open-db", Label: "打开数据库 (Open Database)"},
		objectMatch{Kind: "action", Action: "refresh", Label: "刷新工作区 (Reload)"},
		objectMatch{Kind: "action", Action: "theme", Label: "切换明暗主题 (Toggle Theme)"},
		objectMatch{Kind: "action", Action: "sidebar", Label: "显示/隐藏侧栏 (Toggle Sidebar)"},
		objectMatch{Kind: "action", Action: "details", Label: "显示/隐藏详情 (Toggle Details)"},
		objectMatch{Kind: "action", Action: "privacy", Label: "隐私模式 (Toggle Privacy Mode)"},
		objectMatch{Kind: "action", Action: "erd", Label: "ER 图 (ER Diagram)"},
		objectMatch{Kind: "action", Action: "users", Label: "用户管理 (User Management)"},
	)
	return out
}

// runAction executes an application command listed in the palette.
func (ws *workspace) runAction(action string) {
	switch action {
	case "new-query":
		ws.addQueryTab()
	case "open-db":
		// Palette path has no pill tap to anchor to; drop the sheet under the
		// toolbar's left edge instead.
		ws.openDatabasePicker(fyne.NewPos(120, 40))
	case "refresh":
		ws.reloadTable()
	case "theme":
		darkMode = !darkMode
		applyTheme(ws.window)
	case "sidebar":
		ws.toggleSidebar()
	case "details":
		ws.toggleDetails()
	case "privacy":
		on := togglePrivacyMode()
		ws.refreshRibbon()
		showInfo(ws.window, "隐私模式",
			fmt.Sprintf("隐私模式已%s。\n主机/端口/库名在界面中显示为 •••。",
				map[bool]string{true: "开启", false: "关闭"}[on]))
	case "erd":
		ws.openERDiagram()
	case "users":
		ws.openUserManagement()
	}
}

// quickOpenSearch ranks the candidates and returns the top quickOpenLimit.
// A leading ^ anchors the pattern to the start of the name and a trailing $
// anchors it to the end (TablePlus OpenAnything exact-match syntax); e.g.
// "^order" finds "orders" not "order_items", and "^orders$" is exact.
func (ws *workspace) quickOpenSearch(pattern string) []objectMatch {
	cands := ws.quickOpenCandidates()
	raw := strings.TrimSpace(pattern)
	anchorStart := strings.HasPrefix(raw, "^")
	anchorEnd := strings.HasSuffix(raw, "$")
	needle := strings.ToLower(raw)
	if anchorStart {
		needle = needle[1:]
	}
	if anchorEnd {
		needle = needle[:len(needle)-1]
	}
	type scored struct {
		m objectMatch
		s int
	}
	var ranked []scored
	for _, c := range cands {
		// Lowercase both sides so the palette matches case-insensitively,
		// like TablePlus OpenAnything (e.g. "li" finds "Lima").
		text := strings.ToLower(c.Label)
		if c.Database != "" {
			text = strings.ToLower(c.Database) + "." + text
		}
		var score int
		var ok bool
		if anchorStart || anchorEnd {
			// Anchored matching: prefix / suffix / exact, scored by closeness.
			ok, score = anchoredScore(needle, text, anchorStart, anchorEnd)
		} else {
			score, ok = fuzzyScore(needle, text)
		}
		if !ok {
			continue
		}
		ranked = append(ranked, scored{m: c, s: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].s > ranked[j].s })
	if len(ranked) > quickOpenLimit {
		ranked = ranked[:quickOpenLimit]
	}
	out := make([]objectMatch, len(ranked))
	for i, sc := range ranked {
		out[i] = sc.m
	}
	return out
}

// anchoredScore reports whether text matches an anchored needle and assigns a
// score (exact > prefix+suffix > prefix > suffix) so tighter matches rank
// first.
func anchoredScore(needle, text string, anchorStart, anchorEnd bool) (bool, int) {
	if needle == "" {
		return true, 1 // empty anchored pattern matches everything, low priority
	}
	match := false
	switch {
	case anchorStart && anchorEnd:
		match = text == needle
	case anchorStart:
		match = strings.HasPrefix(text, needle)
	case anchorEnd:
		match = strings.HasSuffix(text, needle)
	}
	if !match {
		return false, 0
	}
	score := 4
	if anchorStart && anchorEnd {
		score = 8
	}
	return true, score - len(text)/8
}

// quickOpenText renders one hit for the list.
func (ws *workspace) quickOpenText(m objectMatch) string {
	switch m.Kind {
	case "connection":
		return "连接 · " + m.Label
	case "table":
		if m.Database != "" {
			return m.Database + " · " + m.Table
		}
		return m.Table
	case "query":
		s := m.SQL
		if len(s) > 60 {
			s = s[:60] + "…"
		}
		return "历史 · " + s
	case "action":
		return "功能 · " + m.Label
	}
	return m.Label
}

// jumpToMatch opens the chosen hit.
func (ws *workspace) jumpToMatch(m objectMatch) {
	switch m.Kind {
	case "connection":
		ws.openConnectionByName(m.ConnName)
	case "table":
		if m.Database != "" && m.Database != ws.currentDB {
			ws.switchDatabase(m.Database)
		}
		ws.openTable(m.Table)
	case "action":
		ws.runAction(m.Action)
	case "query":
		qp := ws.activeQueryPane()
		if qp == nil {
			ws.addQueryTab()
			qp = ws.activeQueryPane()
		}
		if qp == nil {
			return
		}
		if idx := ws.queryTabIndex(qp); idx >= 0 {
			ws.tabs.setActive(idx)
			ws.showTab(idx)
		}
		sql := m.SQL
		safeDo(func() {
			if qp.editor != nil {
				qp.editor.SetText(sql)
			}
		})
	}
}

// queryTabIndex returns the strip index of a query pane, or -1.
func (ws *workspace) queryTabIndex(qp *queryPane) int {
	for i, d := range ws.docs {
		if d.kind == docQuery && d.qp == qp {
			return i
		}
	}
	return -1
}

// openConnectionByName dials a saved connection by name and opens a fresh
// workspace for it, like picking it from the connection breadcrumb.
func (ws *workspace) openConnectionByName(name string) {
	conns, err := loadConnections()
	if err != nil {
		return
	}
	for _, c := range conns {
		if c.Name == name {
			sess, err := dialConn(c)
			if err != nil {
				showInfo(ws.window, "打开连接", "无法连接到 "+name+"：\n"+err.Error())
				return
			}
			ws.skipWelcome = true
			openWorkspace(fyne.CurrentApp(), c, sess, darkMode, ws.welcomeReturn)
			ws.window.Close()
			return
		}
	}
}

// fuzzyScore is a subsequence matcher: every rune of needle must appear in
// haystack in order. Contiguous matches and a leading match score higher;
// very long haystacks are penalised so the tightest name wins.
func fuzzyScore(needle, haystack string) (int, bool) {
	if needle == "" {
		return 0, true
	}
	n := []rune(needle)
	h := []rune(haystack)
	score := 0
	hi := 0
	prev := -2
	for _, rn := range n {
		found := false
		for ; hi < len(h); hi++ {
			if h[hi] == rn {
				if hi == prev+1 {
					score += 5
				} else {
					score += 1
				}
				if hi == 0 {
					score += 3
				}
				prev = hi
				hi++
				found = true
				break
			}
		}
		if !found {
			return 0, false
		}
	}
	score -= len(h) / 8
	return score, true
}
