package main

// 服务器会话（Navicat「会话管理」/ DBeaver Sessions 的 Fyne 形态）：
//
// 内核可选能力 ProcessLister（dbcore v0.5.0）。fynequery 的 session 持有的是
// *sql.DB，用 dbcoremysql.NewFromDB（与 ListIndexes 同模式）现场构造内核驱动
// 再断言能力——MySQL 支持；其它引擎 session 实现自己的桥后自然可用。引擎不
// 支持时弹窗给一行说明。
//
// UI：独立窗口（低频管理操作，不占侧栏 tab）。列表后台拉取（safeDo 回 UI），
// 终止**两步确认**（按钮文字变化提示，未提交事务会回滚）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	dbcore "github.com/Ben-guanzhi/dbcore"

	"fynequery/db"
)

// procListerFor 当前连接的 ProcessLister 能力（nil = 不支持）。
func procListerFor(sess db.Session) dbcore.ProcessLister {
	return db.ProcListerFor(sess)
}

// openServerSessions 弹出会话管理窗口（「更多功能」菜单入口）。
func (ws *workspace) openServerSessions() {
	pl := procListerFor(ws.liveSession())
	if pl == nil {
		showInfo(ws.window, "Server Sessions",
			"当前引擎不支持会话管理（需要 MySQL / PostgreSQL / SQL Server）。")
		return
	}

	status := widget.NewLabel("正在读取会话…")
	status.Truncation = fyne.TextTruncateEllipsis

	type row struct {
		id   string
		text string
	}
	var rows []row

	list := widget.NewList(
		func() int { return len(rows) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i int, o fyne.CanvasObject) {
			if i < 0 || i >= len(rows) {
				return
			}
			lbl := o.(*widget.Label)
			lbl.Truncation = fyne.TextTruncateEllipsis
			lbl.SetText(rows[i].text)
		},
	)
	sel := ""
	list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(rows) {
			sel = rows[id].id
			list.UnselectAll()
		}
	}

	killBtn := widget.NewButton("终止选中", func() {})
	refresh := func() {
		status.SetText("正在读取会话…")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			procs, err := pl.Processes(ctx)
			out := make([]row, 0, len(procs))
			for _, p := range procs {
				out = append(out, row{id: p.ID, text: procRowText(p)})
			}
			safeDo(func() {
				rows = out
				list.Refresh()
				if err != nil {
					status.SetText("读取会话失败：" + err.Error())
				} else {
					status.SetText(fmt.Sprintf("会话 %d 个（不含当前连接）· 选中后可终止", len(out)))
				}
			})
		}()
	}

	// 两步确认：第一次按改按钮文字并提示，第二次同一选中才真正终止。
	killBtn.OnTapped = func() {
		if sel == "" {
			status.SetText("先在列表里选中一个会话")
			return
		}
		if killBtn.Text != "确认终止 "+sel+"？" {
			killBtn.SetText("确认终止 " + sel + "？")
			status.SetText("再按一次确认（未提交事务会回滚）")
			return
		}
		killBtn.SetText("终止选中")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := pl.KillProcess(ctx, sel)
			msg := "已终止 " + sel
			if err != nil {
				msg = "终止失败：" + err.Error()
			}
			safeDo(func() {
				status.SetText(msg)
				refresh()
			})
		}()
	}

	refresh()
	win := fyne.CurrentApp().NewWindow("Server Sessions")
	win.SetContent(container.NewBorder(
		container.NewVBox(status, container.NewHBox(killBtn)),
		nil, nil, nil, list))
	win.Resize(fyne.NewSize(720, 460))
	win.Show()
}

// procRowText 一行摘要：ID · user@client · db · state · 时长 · 语句首行。
func procRowText(p dbcore.ServerProcess) string {
	parts := make([]string, 0, 5)
	who := p.User
	if p.Client != "" {
		who += "@" + p.Client
	}
	if who != "" {
		parts = append(parts, who)
	}
	if p.DB != "" {
		parts = append(parts, p.DB)
	}
	state := p.State
	if state == "" {
		state = p.Command
	}
	if state != "" {
		parts = append(parts, state)
	}
	if p.Time != "" {
		parts = append(parts, p.Time+"s")
	}
	if p.Query != "" {
		q := strings.ReplaceAll(p.Query, "\t", " ")
		r := []rune(q)
		if len(r) > 40 {
			q = string(r[:40]) + "…"
		}
		parts = append(parts, q)
	}
	return strings.Join(parts, " · ")
}
