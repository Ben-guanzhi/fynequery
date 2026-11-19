package main

// 数据生成 UI（DBeaver Mock Data 的 Fyne 形态）：内核 dbcore.GenerateInserts
// 按当前表的列类型生成测试 INSERT，放进 SQL 编辑器——不自动执行。
//
// db.Session 的 Columns() 返回 proto.ColumnInfo（Name/Type/Nullable/Key）——
// 正好是内核 GenOptions 所需的最小列信息（IsPrimaryKey = Key=="PK"）。

import (
	"fmt"
	"strings"
	"time"

	"github.com/Ben-guanzhi/dbcore"
	"fyne.io/fyne/v2/dialog"
)

// generateTestData reads the table's columns, asks the kernel for INSERTs and
// puts them in the SQL editor. Nothing runs until the user runs it.
func (ws *workspace) generateTestData(name string) {
	if ws.sess == nil {
		return
	}
	gen := ws.nextGen()
	progress := dialog.NewProgressInfinite("Generate data", "Reading columns of "+name+"…", ws.window)
	progress.Show()
	safeGo("generate data "+name, func() {
		cols, err := ws.sess.Columns(name)
		safeDo(func() {
			progress.Hide()
			if !ws.current(gen) || ws.sess == nil {
				return
			}
			if err != nil {
				showInfo(ws.window, "Generate data", "Could not read columns:\n"+err.Error())
				return
			}
			kcols := make([]dbcore.ColumnInfo, len(cols))
			for i, c := range cols {
				kcols[i] = dbcore.ColumnInfo{
					Name: c.Name, Type: c.Type, Nullable: c.Nullable,
					IsPrimaryKey: c.Key == "PK",
				}
			}
			dialect := ws.conn.Driver
			seed := uint64(time.Now().UnixNano())
			stmts := dbcore.GenerateInserts(kcols, dbcore.GenOptions{
				Rows: 10, Seed: seed, Dialect: dialect,
				Table: name, SkipPrimaryKey: true,
			})
			if len(stmts) == 0 {
				showInfo(ws.window, "Generate data", "No columns to generate from.")
				return
			}
			// Put the INSERTs in the active query pane's editor (the same
			// path a snippet load uses), so the user reviews before running.
			if qp := ws.activeQueryPane(); qp != nil && qp.editor != nil {
				qp.editor.SetText(strings.Join(stmts, "\n"))
			} else {
				showInfo(ws.window, "Generate data", "Open a SQL tab first, then generate again.")
				return
			}
			showInfo(ws.window, "Generate data",
				fmt.Sprintf("%d INSERT statements placed in the editor (seed %d). Nothing ran.",
					len(stmts), seed))
		})
	})
}
