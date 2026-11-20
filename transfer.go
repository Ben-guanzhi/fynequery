package main

// 数据传输 UI（§358）：内核 dbcore.TransferTable 直驱——把当前连接的一张
// 表搬到目标连接的同名表。源/目标驱动都经 TransferSession 能力接口取
// （内置 MySQL 与插件会话都能给出）；目标连接复用 connpicker 的拨号流。

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
	"github.com/Ben-guanzhi/dbcore"
)

// startTransfer 是表右键「传输到连接…」的入口：先选目标连接（复用
// showConnectionPicker 的拨号流），再进传输对话框确认参数。
func (ws *workspace) startTransfer(tableName string) {
	if ws.sess == nil {
		return
	}
	srcSess, ok := ws.sess.(db.TransferSession)
	if !ok {
		showInfo(ws.window, "Transfer", "当前连接的驱动不支持数据传输（缺内核 SQLDriver 视图）。")
		return
	}
	conns, err := loadConnections()
	if err != nil {
		showInfo(ws.window, "Transfer", "读取连接列表失败："+err.Error())
		return
	}
	showConnectionPicker(ws.window, conns, func(c Connection, dstSess db.Session) {
		dst, ok := dstSess.(db.TransferSession)
		if !ok {
			showInfo(ws.window, "Transfer", fmt.Sprintf("%s 的驱动不支持数据传输。", c.Name))
			return
		}
		ws.showTransferDialog(tableName, srcSess, dst, c)
	})
}

// showTransferDialog 渲染确认对话框：源表 → 目标连接，批量行数可调。
// 确认后在后台跑内核 TransferTable，进度对话框显示行数。
func (ws *workspace) showTransferDialog(table string, src, dst db.TransferSession, target Connection) {
	batch := widget.NewEntry()
	batch.SetText("200")

	info := widget.NewLabel(fmt.Sprintf("把 %s.%s 的数据传到 %s 的同名表（目标表需已存在，列按名匹配）。",
		ws.conn.Name, table, target.Name))
	info.Wrapping = fyne.TextWrapBreak

	var running bool
	dlg := dialog.NewForm("Transfer data", "传输", "取消",
		[]*widget.FormItem{
			widget.NewFormItem("说明", info),
			widget.NewFormItem("每批行数", batch),
		},
		func(confirm bool) {
			if !confirm || running {
				return
			}
			n := 0
			fmt.Sscanf(batch.Text, "%d", &n)
			ws.runTransfer(table, src, dst, target, n)
		}, ws.window)
	dlg.Resize(fyne.NewSize(460, 220))
	dlg.Show()
}

// runTransfer 在后台执行内核 TransferTable：进度对话框实时显示行数，
// 完成后报总数；失败显示已传行数与错误。
func (ws *workspace) runTransfer(table string, src, dst db.TransferSession, target Connection, batchSize int) {
	progress := dialog.NewProgressInfinite("Transfer", "Transferring "+table+"…", ws.window)
	progress.Show()
	gen := ws.nextGen()
	var total int64
	safeGo("transfer."+table, func() {
		var srcDrv, dstDrv dbcore.SQLDriver
		var err error
		srcDrv, err = src.TransferDriver()
		if err == nil {
			dstDrv, err = dst.TransferDriver()
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()
				total, err = dbcore.TransferTable(ctx, srcDrv, dstDrv,
					dbcore.TransferSpec{Table: table, BatchSize: batchSize},
					func(p dbcore.TransferProgress) {
						// 进度回调来自内核 goroutine——行数在完成后统一报。
						_ = p
					})
			}
		}
		safeDo(func() {
			progress.Hide()
			if !ws.current(gen) {
				return
			}
			if err != nil {
				dialog.NewError(fmt.Errorf("传输中止: %v", err), ws.window).Show()
				return
			}
			showInfo(ws.window, "Transfer", fmt.Sprintf("完成：%d 行已传到 %s.%s。", total, target.Name, table))
		})
	})
}
