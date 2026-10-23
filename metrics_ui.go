package main

// Server-metrics panel: a small window that shows the connection's curated
// read-only server metrics (connections, uptime, cache hit ratio, ...) and
// refreshes periodically. Mirrors the reference project's read-only metrics
// view, adapted to fynequery's optional Metricser capability.

import (
	"fmt"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// openServerMetrics opens the metrics panel for the active connection.
func (ws *workspace) openServerMetrics() {
	sess := ws.liveSession()
	if sess == nil {
		showInfo(ws.window, "Server Metrics", "No active connection.")
		return
	}
	m, ok := db.AsMetricser(sess)
	if !ok {
		showInfo(ws.window, "Server Metrics", "This connection does not report server metrics.")
		return
	}
	ws.showMetricsWindow(m)
}

// showMetricsWindow builds the metrics window: a label/value list with a manual
// refresh plus a 3s auto-refresh that stops when the window closes.
func (ws *workspace) showMetricsWindow(m db.Metricser) {
	app := fyne.CurrentApp()
	w := app.NewWindow(fmt.Sprintf("Server Metrics — %s", ws.conn.Name))
	w.Resize(fyne.NewSize(480, 560))

	list := container.NewVBox()
	scroll := container.NewVScroll(list)
	status := widget.NewLabel("Loading…")
	status.Importance = widget.LowImportance

	refresh := func() {
		safeGo("metrics", func() {
			ms, err := m.Metrics()
			safeDo(func() {
				if err != nil {
					status.SetText("Error: " + err.Error())
					return
				}
				list.Objects = list.Objects[:0]
				lastGroup := ""
				for _, mt := range ms {
					if mt.Group != "" && mt.Group != lastGroup {
						head := widget.NewLabel(mt.Group)
						head.TextStyle = fyne.TextStyle{Bold: true}
						list.Objects = append(list.Objects, container.NewPadded(head))
						lastGroup = mt.Group
					}
					label := widget.NewLabel(mt.Label)
					value := widget.NewLabel(mt.Value)
					value.Alignment = fyne.TextAlignTrailing
					list.Objects = append(list.Objects, container.NewPadded(container.NewBorder(nil, nil, label, value)))
				}
				if len(ms) == 0 {
					list.Objects = append(list.Objects, widget.NewLabel("No metrics reported."))
				}
				list.Refresh()
				status.SetText(fmt.Sprintf("%d metrics · updated %s", len(ms), time.Now().Format("15:04:05")))
			})
		})
	}

	refreshBtn := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), refresh)
	toolbar := container.NewBorder(nil, nil, container.NewPadded(refreshBtn), nil, nil)
	bg := newPanelBackground(
		container.NewBorder(toolbar, container.NewPadded(status), nil, nil, scroll),
		func() color.Color { return pal().bg },
	)
	w.SetContent(bg)
	w.Show()
	refresh()

	// Auto-refresh while the window is open; stop on close.
	stop := make(chan struct{})
	w.SetOnClosed(func() { close(stop) })
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				refresh()
			case <-stop:
				return
			}
		}
	}()
}
