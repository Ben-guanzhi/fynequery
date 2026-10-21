package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// CSV import, the other half of the export the grid already had: right-click a
// table, pick a file, and the rows land in one transaction.
//
// The one rule this dialog is built around is that the preview and the run are
// the same batch list. The build reads the catalog and the file once, in the
// background, and the preview only decides how much of it to show - so what the
// user reads is what gets executed, and a file cannot change meaning between
// the two.

// importPreviewStatements is how many statements the preview prints. A
// thousand-row file is two statements of 500, but a narrow table can be
// twenty, and a preview that renders all of them is a wall of literals nobody
// reads.
const importPreviewStatements = 20

// localFilePath turns a chosen URI into a path os.Open accepts. Windows file
// URIs carry a slash before the drive letter, which os.Open then reads as a
// relative path.
func localFilePath(uri fyne.URI) string {
	p := uri.Path()
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		return p[1:]
	}
	return p
}

// importCSVDialog asks which file to import into the right-clicked table. A
// view has no rows to insert into, and an import without a session has nowhere
// to send them, so both are answered here rather than after the file is read.
func (c *Client) importCSVDialog(nd *nodeData) {
	if nd.typ == "view" {
		c.setStatusWarn("视图不能导入数据")
		return
	}
	id := c.selectedOrLive()
	if id == "" {
		c.setStatusWarn("请先连接一个数据库")
		return
	}
	fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			c.setStatusError("导入失败：" + err.Error())
			return
		}
		if r == nil {
			return // cancelled
		}
		defer r.Close()
		c.askImportOptions(id, nd, localFilePath(r.URI()))
	}, c.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".csv", ".txt"}))
	fd.Resize(fyne.NewSize(640, 480))
	fd.Show()
}

// askImportOptions asks the one question the file cannot answer about itself:
// is its first row column names or data. Guessing later would mean a header
// mismatch that reads as "the table has no column named id, name".
func (c *Client) askImportOptions(id string, nd *nodeData, path string) {
	header := widget.NewCheck("第一行是列名", nil)
	header.SetChecked(true)

	next := widget.NewButtonWithIcon("下一步", theme.ConfirmIcon(), nil)
	next.Importance = widget.HighImportance
	var d *dialog.CustomDialog
	next.OnTapped = func() {
		d.Hide()
		c.buildImportPreview(id, nd, path, header.Checked)
	}
	body := container.NewVBox(
		widget.NewLabel("目标："+importTargetLabel(nd)),
		widget.NewLabel(filepath.Base(path)),
		header,
	)
	content := container.NewBorder(nil, next, nil, nil, body)
	d = dialog.NewCustom("导入 CSV", "取消", content, c.win)
	d.Resize(fyne.NewSize(420, 180))
	d.Show()
}

// importTargetLabel names the table the way the engine addresses it, so the
// dialog says which of the connection's databases is about to change.
func importTargetLabel(nd *nodeData) string {
	label := nd.name
	if nd.schema != "" {
		label = nd.schema + "." + label
	}
	if nd.db != "" {
		label = nd.db + "." + label
	}
	return label
}

// buildImportPreview renders the whole file into statements off the UI thread,
// then shows them. Rendering them all here - not the first twenty - is what
// keeps the preview honest: the catalog is read once and the same slice is
// what 执行 sends.
func (c *Client) buildImportPreview(id string, nd *nodeData, path string, hasHeader bool) {
	req := db.CSVImportRequest{
		ConnID:    id,
		Database:  nd.db,
		Schema:    nd.schema,
		Table:     nd.name,
		HasHeader: hasHeader,
		Path:      path,
	}
	var (
		batches  []db.ImportBatch
		buildErr error
	)
	c.setStatus("正在读取文件 ...")
	// Nothing owns this round trip's cancel: it is one catalog read and one
	// file read, and there is no dialog of its own to close. The preview's
	// dialog - the one that can be abandoned - comes after it.
	c.background(func(ctx context.Context) {
		batches, buildErr = c.store.BuildCSVImport(ctx, req)
	}, func() { c.showImportPreview(req, batches, buildErr) })
}

// importRowCount is how many rows the batch list would insert, which is the
// number the header says - not the number of statements, which is an artefact
// of how the engine chunks them.
func importRowCount(batches []db.ImportBatch) int {
	n := 0
	for _, b := range batches {
		n += b.Rows
	}
	return n
}

// importPreviewText renders the batch list for the preview: the first
// importPreviewStatements statements, and a line saying how many are not
// shown. It is a slice of the very batches 执行 will run, never a second,
// shorter build of them - which is what makes "the rest" a statement about
// the display rather than about the import.
func importPreviewText(batches []db.ImportBatch) string {
	shown := batches
	omitted := 0
	if len(shown) > importPreviewStatements {
		shown = batches[:importPreviewStatements]
		omitted = len(batches) - importPreviewStatements
	}
	parts := make([]string, 0, len(shown)+1)
	for _, b := range shown {
		parts = append(parts, b.Statement+";")
	}
	if omitted > 0 {
		parts = append(parts, fmt.Sprintf("… 其余 %d 条语句已省略（执行时会全部执行）", omitted))
	}
	return strings.Join(parts, "\n\n")
}

// showImportPreview puts the statements in front of the user with the one
// button that can write them. UI thread only.
func (c *Client) showImportPreview(req db.CSVImportRequest, batches []db.ImportBatch, err error) {
	if err != nil {
		c.setStatusError("导入失败：" + err.Error())
		dialog.ShowError(err, c.win)
		return
	}

	head := widget.NewLabel(fmt.Sprintf("目标表 %s · 将插入 %d 行 · 分 %d 条语句执行",
		req.Table, importRowCount(batches), len(batches)))

	preview := widget.NewMultiLineEntry()
	preview.TextStyle = fyne.TextStyle{Monospace: true}
	preview.Wrapping = fyne.TextWrapOff
	preview.Disable()
	preview.SetText(importPreviewText(batches))

	var (
		d        *dialog.CustomDialog
		giveUp   func()
		applying bool
	)
	applyBtn := widget.NewButtonWithIcon("执行", theme.ConfirmIcon(), nil)
	applyBtn.Importance = widget.HighImportance
	applyBtn.OnTapped = func() {
		// An import is a transaction, not a keystroke: a second round would
		// insert everything twice. The button is the only way in, so it goes
		// away for the duration.
		if applying {
			return
		}
		applying = true
		applyBtn.Disable()
		c.setStatus("导入中 ...")
		var (
			applied int64
			appErr  error
		)
		giveUp = c.background(func(ctx context.Context) {
			applied, appErr = c.store.ExecImportBatches(ctx, req, batches)
		}, func() {
			applying = false
			if appErr != nil {
				applyBtn.Enable()
				c.setStatusError("导入失败：" + appErr.Error())
				dialog.ShowError(appErr, c.win)
				return
			}
			// The import deliberately stays out of the history log: a few
			// thousand INSERTs would push every statement the user actually
			// ran out of the 500-entry log.
			c.setStatus(fmt.Sprintf("已导入 %d 行到 %s", applied, req.Table))
			d.Hide()
			if c.grid.src.Table != "" && c.grid.src.Table == req.Table {
				c.runAllStatements() // the grid is showing this table
			}
		})
	}

	content := container.NewBorder(head, container.NewHBox(applyBtn), nil, nil,
		container.NewScroll(preview))
	d = dialog.NewCustom("导入 CSV", "取消", content, c.win)
	d.SetOnClosed(func() {
		if giveUp != nil {
			giveUp()
		}
	})
	d.Resize(fyne.NewSize(640, 420))
	d.Show()
	c.setStatus("就绪")
}
