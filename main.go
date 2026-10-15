package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

// appVersion shows in the window title so a stale executable is obvious. A
// release build overwrites it with the tag (`-ldflags -X main.appVersion=...`),
// so what the title bar says and what was published are the same string.
var appVersion = "1.3.0"

func main() {
	a := app.NewWithID("com.tableprogo.fyne")

	w := a.NewWindow("TablePro Fyne · 数据库查询客户端 v" + appVersion)
	c := newClient(a, w)
	applyTheme(c.uiSet.Theme)
	w.SetContent(c.buildUI())
	// Focus sticks only once the content exists; do it here so the editor
	// owns the keyboard the moment the window opens.
	w.Canvas().Focus(c.tabs.entry)

	if c.uiSet.WindowWidth > 100 && c.uiSet.WindowHeight > 100 {
		w.Resize(fyne.NewSize(c.uiSet.WindowWidth, c.uiSet.WindowHeight))
	} else {
		w.Resize(fyne.NewSize(1280, 820))
	}
	w.SetCloseIntercept(func() {
		c.saveWindowSettings(w)
		c.closeAll()
		w.Close()
	})
	w.ShowAndRun()
}
