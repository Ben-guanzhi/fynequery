// TablePro-style database GUI built with Fyne.
// Welcome screen with the saved connection list, then a workspace with a
// sidebar tree, data grid, SQL editor and status bar. MySQL is built in;
// PostgreSQL / SQL Server / Oracle arrive as plugin executables.
package main

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"fynequery/db"
)

// spacerW is a fixed-width horizontal spacer.
func spacerW(w float32) fyne.CanvasObject {
	return layout2fixed(w)
}

type fixedSpacer struct {
	widget.BaseWidget
	w float32
}

func layout2fixed(w float32) *fixedSpacer {
	s := &fixedSpacer{w: w}
	s.ExtendBaseWidget(s)
	return s
}

func (s *fixedSpacer) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewStack())
}

func (s *fixedSpacer) MinSize() fyne.Size { return fyne.NewSize(s.w, 1) }

// spacerH is a fixed-height vertical spacer.
func spacerH(h float32) fyne.CanvasObject {
	return layoutFixedV(h)
}

type fixedVSpacer struct {
	widget.BaseWidget
	h float32
}

func layoutFixedV(h float32) *fixedVSpacer {
	s := &fixedVSpacer{h: h}
	s.ExtendBaseWidget(s)
	return s
}

func (s *fixedVSpacer) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewStack())
}

func (s *fixedVSpacer) MinSize() fyne.Size { return fyne.NewSize(1, s.h) }

// applyTheme swaps the light/dark theme and refreshes the window.
// The Fyne theme is app-level, so it is only pushed to Fyne when the variant
// actually changed — re-pushing it makes Fyne walk every open window's native
// HWND (setDarkMode), which panics on windows that are not (yet) shown.
var lastAppliedVariant int = -1

// themeChangeHooks recolor custom-drawn surfaces after a light/dark switch.
// Fyne auto-recolours its own widgets on SetTheme, but a bare canvas.Text /
// canvas.Rectangle keeps the colour it was built with; such surfaces register a
// recolor callback here. Callbacks are idempotent and safe to re-run even for
// windows that are no longer visible.
var themeChangeHooks []func()

func onThemeChange(fn func()) { themeChangeHooks = append(themeChangeHooks, fn) }

func applyTheme(w fyne.Window) {
	variant := 0
	if darkMode {
		variant = 1
	}
	if variant != lastAppliedVariant {
		lastAppliedVariant = variant
		fyne.CurrentApp().Settings().SetTheme(&tablePlusTheme{base: theme.DefaultTheme()})
	}
	if w != nil {
		w.Content().Refresh()
	}
	for _, fn := range themeChangeHooks {
		fn()
	}
}

// fitWindowToScreen re-applies size once the window is shown: Fyne sets the
// native size before it can detect the monitor DPI scale, so on scaled
// displays the canvas ends up larger than the real client area (content
// clipped at the right/bottom and the toolbar painted off-position). The
// requested size is clamped to the monitor work area, then the window is
// centred so it cannot open partly off-screen.
func fitWindowToScreen(w fyne.Window, size fyne.Size) {
	size = clampWindowSize(w.Canvas().Scale(), size)
	// Once the native caption is stripped, a Fyne resize would re-add the
	// removed margins around the canvas; resize the real window instead.
	if chromeFitWindow(w, size) {
		return
	}
	w.Resize(size)
	w.CenterOnScreen()
}

// clampWindowSize limits a window content size to the primary monitor work
// area, leaving room for the window frame and a visible margin.
func clampWindowSize(scale float32, size fyne.Size) fyne.Size {
	const margin = 48
	if avail := screenWorkArea(scale); avail.Width > 0 && avail.Height > 0 {
		if maxW := avail.Width - margin; size.Width > maxW {
			size.Width = maxW
		}
		if maxH := avail.Height - margin; size.Height > maxH {
			size.Height = maxH
		}
	}
	return size
}

// fitWindowWhenScaled applies the size immediately (so the first frame is not
// clipped) and again after the first frame, when Fyne has detected and
// rounded the monitor scale — until then Canvas().Scale() reports 1 and the
// work-area clamp would use the wrong units.
func fitWindowWhenScaled(w fyne.Window, size fyne.Size) {
	fitWindowToScreen(w, size)
	time.AfterFunc(250*time.Millisecond, func() {
		safeDo(func() { fitWindowToScreen(w, size) })
	})
}

// setupLogging routes plugin diagnostics into a log file next to the config.
func setupLogging() *os.File {
	f, err := os.OpenFile(filepath.Join(configDir(), "fynequery.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	db.SetLogger(func(format string, args ...interface{}) {
		fmt.Fprintf(f, format+"\n", args...)
	})
	return f
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			writeCrash("main", r)
		}
	}()

	directConn := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "-conn":
			directConn = true
		case "-dark":
			darkMode = true
		}
	}

	os.MkdirAll(configDir(), 0o755)
	if f := setupLogging(); f != nil {
		defer f.Close()
	}

	a := app.NewWithID("com.tablepro.fynequery")
	a.Settings().SetTheme(&tablePlusTheme{base: theme.DefaultTheme()})
	a.Lifecycle().SetOnStopped(db.Shutdown)

	// Discover plugin drivers: next to the exe, or ./plugins in dev mode.
	if exe, err := os.Executable(); err == nil {
		db.DiscoverPlugins(filepath.Join(filepath.Dir(exe), "plugins"))
	} else {
		db.DiscoverPlugins("")
	}

	// showWelcome (re)creates the welcome window. Used at startup and again
	// when a workspace window closes (TablePlus returns to the welcome page).
	var showWelcome func()
	showWelcome = func() {
		ww := a.NewWindow("TablePro")
		ww.Resize(fyne.NewSize(1020, 680))
		wp := newWelcomePage(ww, func(c Connection, sess db.Session) {
			// Double-click / pick a connection: open the workspace and close
			// the welcome window so the two don't stack.
			openWorkspace(a, c, sess, darkMode, showWelcome)
			ww.Close()
		})
		ww.SetContent(newPanelBackground(wp.build(), func() color.Color { return pal().bg }))
		applyTheme(ww)
		ww.Show()
		fitWindowWhenScaled(ww, fyne.NewSize(1020, 680))
	}

	if directConn {
		// Open the first saved connection; on failure fall back to welcome.
		launchDirect := func() {
			if conns, _ := loadConnections(); len(conns) > 0 {
				c := conns[0]
				sess, err := dialConn(c)
				if err == nil {
					openWorkspace(a, c, sess, darkMode, showWelcome)
					return
				}
				fmt.Fprintf(os.Stderr, "open %q: %v\n", c.Name, err)
			}
			showWelcome()
		}
		if hasPasscode() {
			showLockWindow(a, launchDirect)
		} else {
			launchDirect()
		}
		a.Run()
		return
	}

	if hasPasscode() {
		showLockWindow(a, showWelcome)
	} else {
		showWelcome()
	}
	a.Run()
}
