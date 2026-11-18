package main

// 数据字典导出（「更多功能」菜单）：内核 ExportDictionary（dbcore v0.12.0）
// 的 fynequery 接入——把当前库的结构导出为 Markdown 文档。
//
// 内核签名的目录面是 DictionarySource（三个方法）；fynequery 的 Session 只
// 暴露 Tables/Columns（插件会话没有外键面），所以用一个薄适配把 Session 包成
// DictionarySource——**不必为了导出字典实现整个 SQLDriver**。外键面返回空，
// 字典里的「键」列只有 PK。

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"

	dbcore "github.com/Ben-guanzhi/dbcore"
	"fynequery/db"
)

// dictSource 把 Session 的目录能力适配成内核的 DictionarySource。
type dictSource struct{ s db.Session }

func (d dictSource) ListTables(_ context.Context, _ string) ([]dbcore.TableInfo, error) {
	tabs, err := d.s.Tables()
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.TableInfo, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, dbcore.TableInfo{Name: t.Name, Type: t.Kind, Rows: t.Rows})
	}
	return out, nil
}

func (d dictSource) ListColumns(_ context.Context, _, table string) ([]dbcore.ColumnInfo, error) {
	cols, err := d.s.Columns(table)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ColumnInfo, 0, len(cols))
	for _, c := range cols {
		out = append(out, dbcore.ColumnInfo{
			Name:         c.Name,
			Type:         c.Type,
			Nullable:     c.Nullable,
			IsPrimaryKey: c.Key == "PK",
		})
	}
	return out, nil
}

// ListForeignKeys：插件会话的 proto 没有外键面——字典里就没有 FK 标注
// （不是错误，是能力的诚实边界）。
func (d dictSource) ListForeignKeys(_ context.Context, _, _ string) ([]dbcore.ForeignKeyInfo, error) {
	return nil, nil
}

// exportDictionary 生成当前库的数据字典并让用户选保存位置。
func (ws *workspace) exportDictionary() {
	if ws.sess == nil {
		dialog.NewInformation("数据字典", "先连接一个数据库。", ws.window).Show()
		return
	}
	sess := ws.sess
	safeGo("export dictionary", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		doc, err := dbcore.ExportDictionary(ctx, dictSource{s: sess}, "", nil)
		safeDo(func() {
			if err != nil {
				dialog.NewError(err, ws.window).Show()
				return
			}
			name := fmt.Sprintf("data-dictionary-%s.md", time.Now().Format("20060102"))
			save := dialog.NewFileSave(func(wc fyne.URIWriteCloser, werr error) {
				if werr != nil {
					dialog.NewError(werr, ws.window).Show()
					return
				}
				if wc == nil {
					return
				}
				if _, werr := wc.Write([]byte(doc)); werr != nil {
					dialog.NewError(werr, ws.window).Show()
				}
				if cerr := wc.Close(); cerr != nil {
					dialog.NewError(cerr, ws.window).Show()
				}
			}, ws.window)
			save.SetFilter(storage.NewExtensionFileFilter([]string{".md"}))
			save.SetFileName(name)
			save.Show()
		})
	})
}

