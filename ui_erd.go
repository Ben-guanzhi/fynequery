package main

// ER 图（P4）：把内置版 erd.go 的能力搬进来 —— 表画成方框，外键画成连线，
// 一眼看出库里表与表之间的关系。独立版原本完全没有这个视图。
//
// 与内置版的两点不同：
//  1. 取外键走独立版 internal/db 的 ForeignKeys（跨 5 引擎），不照搬内置版
//     的 fetchERForeignKeys（那套依赖它自己的 MySQL-only 目录查询）；
//  2. 内置版是可拖拽的自由布局，这里先做**只读网格布局**：位置由表顺序算出来，
//     不做拖拽交互。理由是拖拽需要一套命中/位移/重连线的状态管理，而跨引擎
//     取值已经占了这次移植的主要风险，交互留到确认布局效果之后再补。

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// erdMaxTables 是画进图里的表上限：外键要逐表取，表太多会把目录拖慢。
const erdMaxTables = 40

// erd 布局常量（只读网格布局）。
const (
	erdBoxW   = 180
	erdBoxH   = 34
	erdGapX   = 44
	erdGapY   = 30
	erdPerRow = 4
)

// showERDiagram 取给定库（可带 schema）的表与外键，然后画出 ER 图。
// 目录访问全部在后台跑，单张表的外键取失败不阻断整图。
func (c *Client) showERDiagram(database, schema string) {
	if c.currentID == "" {
		dialog.ShowInformation("ER 图", "请先连接一个数据库。", c.win)
		return
	}
	var (
		tables   []db.TableInfo
		fks      = make(map[string][]db.ForeignKeyInfo)
		fetchErr error
	)
	c.background(func(ctx context.Context) {
		var err error
		tables, err = c.store.Tables(ctx, c.currentID, database, schema)
		if err != nil {
			fetchErr = err
			return
		}
		if len(tables) > erdMaxTables {
			tables = tables[:erdMaxTables]
		}
		for _, t := range tables {
			list, err := c.store.ForeignKeys(ctx, c.currentID, database, t.Schema, t.Name)
			if err != nil {
				continue
			}
			fks[t.Name] = list
		}
	}, func() {
		if fetchErr != nil {
			dialog.ShowError(fetchErr, c.win)
			return
		}
		c.showERDiagramBody(tables, fks)
	})
}

// showERDiagramBody 渲染 ER 图：方框是表，连线是外键（从本表右缘到引用表左缘）。
func (c *Client) showERDiagramBody(tables []db.TableInfo, fks map[string][]db.ForeignKeyInfo) {
	if len(tables) == 0 {
		dialog.ShowInformation("ER 图", "当前库里没有表。", c.win)
		return
	}

	pos := make(map[string]fyne.Position, len(tables))
	objs := make([]fyne.CanvasObject, 0, len(tables)*2)

	for i, t := range tables {
		p := fyne.NewPos(
			float32(i%erdPerRow)*(erdBoxW+erdGapX),
			float32(i/erdPerRow)*(erdBoxH+erdGapY),
		)
		pos[t.Name] = p

		rect := canvas.NewRectangle(detailsBG())
		rect.CornerRadius = 6
		rect.StrokeColor = crumbBorder()
		rect.StrokeWidth = 1
		rect.Move(p)
		rect.Resize(fyne.NewSize(erdBoxW, erdBoxH))

		lbl := widget.NewLabel(t.Name)
		lbl.Truncation = fyne.TextTruncateEllipsis
		lbl.Move(p.Add(fyne.NewPos(8, 8)))
		lbl.Resize(fyne.NewSize(erdBoxW-16, erdBoxH-16))

		objs = append(objs, rect, lbl)
	}

	// 连线画在方框之后，压在框上面（同一层里后加的先画，这里够用）。
	edge := 0
	for name, list := range fks {
		from, ok := pos[name]
		if !ok {
			continue
		}
		for _, fk := range list {
			to, ok := pos[fk.RefTable]
			if !ok {
				continue // 引用表被截断在上限之外，或不在本库
			}
			line := canvas.NewLine(theme.Color(theme.ColorNamePrimary))
			line.StrokeWidth = 1
			line.Position1 = fyne.NewPos(from.X+erdBoxW, from.Y+erdBoxH/2)
			line.Position2 = fyne.NewPos(to.X, to.Y+erdBoxH/2)
			objs = append(objs, line)
			edge++
		}
	}

	rows := (len(tables) + erdPerRow - 1) / erdPerRow
	content := container.NewWithoutLayout(objs...)
	content.Resize(fyne.NewSize(
		float32(erdPerRow)*(erdBoxW+erdGapX),
		float32(rows)*(erdBoxH+erdGapY),
	))

	d := dialog.NewCustom("ER 图", "关闭", container.NewScroll(content), c.win)
	d.Resize(fyne.NewSize(900, 620))
	d.Show()
}
