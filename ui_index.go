package main

// 索引页（P3）：把表的索引列表常驻显示，不再只在「查看结构」对话框里出现。
//
// 数据来独立版 dialect 的 indexes（跨 5 个引擎），不照搬内置版写死的 MySQL
// information_schema.STATISTICS —— 那条查询在非 MySQL 上恒空。列头沿用结构
// 对话框里那套（索引/类型/约束/字段），两处读起来一致。

import (
	"context"
	"strings"

	"fyne.io/fyne/v2"

	"tablepro-fyne/internal/db"
)

// detailsMode 是右侧面板当前显示的内容。
type detailsMode int

const (
	// detailsModeInfo 是「详情」：选中行的字段值，或表的目录计数。
	detailsModeInfo detailsMode = iota
	// detailsModeIndex 是「索引」：当前表的索引列表。
	detailsModeIndex
)

// detailTabIndex 是「索引」标签在 AppTabs 里的下标，切换时用来判定模式。
const detailTabIndex = 1

// setDetailsIndexes 取当前表的索引并渲染；没有打开的表或读取失败都给说明文字。
func (c *Client) setDetailsIndexes() {
	src := c.grid.src
	if src.Table == "" {
		c.details.indexBody.Objects = []fyne.CanvasObject{
			hintLabel("打开一个表后，这里会列出它的索引。"),
		}
		c.details.indexBody.Refresh()
		return
	}
	c.details.indexBody.Objects = []fyne.CanvasObject{hintLabel("加载中 ...")}
	c.details.indexBody.Refresh()

	var (
		indexes []db.IndexInfo
		err     error
	)
	c.background(func(ctx context.Context) {
		indexes, err = c.store.TableIndexes(ctx, c.currentID, src.Database, src.Schema, src.Table)
	}, func() {
		// 取数据期间可能已经切回「详情」或关掉了面板。
		if !c.details.on || c.currentDetailsMode() != detailsModeIndex {
			return
		}
		if err != nil {
			c.details.indexBody.Objects = []fyne.CanvasObject{
				hintLabel("索引读取失败：" + err.Error()),
			}
			c.details.indexBody.Refresh()
			return
		}
		if len(indexes) == 0 {
			c.details.indexBody.Objects = []fyne.CanvasObject{
				hintLabel("该表没有索引。"),
			}
			c.details.indexBody.Refresh()
			return
		}
		c.details.indexBody.Objects = []fyne.CanvasObject{c.indexTable(indexes)}
		c.details.indexBody.Refresh()
	})
}

// indexTable 渲染索引列表，列头与结构对话框保持一致。
func (c *Client) indexTable(indexes []db.IndexInfo) fyne.CanvasObject {
	rows := make([][]string, 0, len(indexes))
	for _, idx := range indexes {
		flags := ""
		switch {
		case idx.Primary:
			flags = "PK"
		case idx.Unique:
			flags = "UNIQUE"
		}
		rows = append(rows, []string{
			idx.Name, idx.Kind, flags, strings.Join(idx.Columns, ", "),
		})
	}
	return newMetaTable([]string{"索引", "类型", "约束", "字段"}, rows)
}
