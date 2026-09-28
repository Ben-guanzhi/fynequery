package main

// Node action dialogs for the navigator: the right-click menu and the
// drop/truncate/rename confirm-and-run flow. Statement rendering is the
// data layer's job (db.TableActionStatement); this file only shows dialogs.

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// showTableNodeMenu pops the drop/truncate/rename menu for one table row.
func (c *Client) showTableNodeMenu(uid string, pos fyne.Position) {
	nd, ok := c.treeNodes[uid]
	if !ok {
		return
	}
	items := []*fyne.MenuItem{
		fyne.NewMenuItem("查看结构", func() { c.showStructureDialog(nd) }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("删除 (DROP)", func() { c.confirmNodeAction(nd, db.ActionDrop, "") }),
	}
	if nd.typ != "view" {
		items = append(items, fyne.NewMenuItem("清空 (TRUNCATE)", func() {
			c.confirmNodeAction(nd, db.ActionTruncate, "")
		}))
	}
	items = append(items, fyne.NewMenuItem("重命名 (RENAME)", func() { c.renameNodeDialog(nd) }))
	widget.NewPopUpMenu(fyne.NewMenu("", items...), c.win.Canvas()).ShowAtPosition(pos)
}

// confirmNodeAction renders the statement first, asks, then runs it.
func (c *Client) confirmNodeAction(nd *nodeData, action, newName string) {
	id := c.selectedOrLive()
	conn, err := c.store.OpenConn(id)
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	stmt, err := db.TableActionStatement(db.ObjectNames{
		DbType:     conn.Config.Type,
		ObjectType: nd.typ,
		Database:   nd.db,
		Schema:     db.DefaultSchema(conn.Config.Type, nd.schema),
		Table:      nd.name,
		NewName:    newName,
	}, action)
	if err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	dialog.ShowConfirm("确认执行", "将执行：\n\n"+stmt+"\n\n该操作不可恢复。", func(ok bool) {
		if !ok {
			return
		}
		run, err := c.store.RunTableAction(id, nd.db, nd.schema, nd.name, action, newName)
		if err != nil {
			dialog.ShowError(err, c.win)
			return
		}
		c.setStatus("已执行：" + run)
		c.refreshSchema()
	}, c.win)
}

// renameNodeDialog asks for the new name, then runs the rename flow.
func (c *Client) renameNodeDialog(nd *nodeData) {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("新的名称（仅限标识符字符）")
	var d *dialog.CustomDialog
	form := widget.NewForm(widget.NewFormItem("重命名 "+nd.name, entry))
	form.OnSubmit = func() {
		d.Hide()
		c.confirmNodeAction(nd, db.ActionRename, entry.Text)
	}
	form.SubmitText = "下一步"
	d = dialog.NewCustom("重命名", "取消", form, c.win)
	d.Resize(fyne.NewSize(380, 150))
	d.Show()
}
