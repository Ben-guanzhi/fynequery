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
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/lang"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dbcore/dbcore/usability"

	"fynequery/db"
)

// Fyne's widget.Select default placeholder "(Select one)" has no zh-Hans
// translation, so on a Chinese system every Select logs a "Translation
// failure" the moment it is constructed (NewSelect looks the key up in its
// constructor). Register the missing key at package init — before any Select
// exists, and independently of a running app — so the lookup succeeds and empty
// selects show a Chinese placeholder. This also silences the warning in tests,
// which build their own app.
func init() {
	_ = lang.AddTranslationsForLocale([]byte(`{"(Select one)":"（请选择）"}`), fyne.Locale("zh-Hans"))
}

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
	// The re-apply is a one-shot timer, but the window may close before it
	// fires (FQ-10): arm it through the window-timer group so the close cancels
	// it instead of letting the callback touch a dead window.
	armWindowTimer(w, 250*time.Millisecond, func() {
		safeDo(func() { fitWindowToScreen(w, size) })
	})
}

// windowTimers tracks the one-shot window timers the UI arms, grouped by
// window, so closing a window cancels every timer still pending for it (FQ-10:
// the old shape dropped the *time.Timer and the callback kept firing into a
// window that was already gone). Fyne has no AddOnClosed, so each group shares
// one OnClosed that stops and forgets the whole group.
var (
	windowTimersMu sync.Mutex
	windowTimers   = map[fyne.Window][]*time.Timer{}
)

// armWindowTimer schedules f to run d later and stops it — along with every
// other timer still pending for w — when w closes.
func armWindowTimer(w fyne.Window, d time.Duration, f func()) {
	windowTimersMu.Lock()
	defer windowTimersMu.Unlock()
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		windowTimersMu.Lock()
		if list, ok := windowTimers[w]; ok {
			for i, cur := range list {
				if cur == t {
					list = append(list[:i], list[i+1:]...)
					break
				}
			}
			if len(list) == 0 {
				delete(windowTimers, w)
			} else {
				windowTimers[w] = list
			}
		}
		windowTimersMu.Unlock()
		f()
	})
	windowTimers[w] = append(windowTimers[w], t)
	// Closing the window cancels every pending timer for it. Each arm call
	// re-registers this cleanup because Fyne exposes SetOnClosed, not
	// AddOnClosed; the closures are equivalent and stop the whole group.
	w.SetOnClosed(func() {
		windowTimersMu.Lock()
		for _, cur := range windowTimers[w] {
			cur.Stop()
		}
		delete(windowTimers, w)
		windowTimersMu.Unlock()
	})
}

// appLogSink is the file sink setupLogging installs; nil until then (and when
// the log file cannot be opened), in which case diagnostics are dropped.
var appLogSink func(string, ...interface{})

// appLogf records a diagnostic in fynequery.log next to the config — the same
// sink the db layer logs through.
func appLogf(format string, args ...interface{}) {
	if appLogSink != nil {
		appLogSink(format, args...)
	}
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
	appLogSink = func(format string, args ...interface{}) {
		fmt.Fprintf(f, format+"\n", args...)
	}
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
	// Usability metrics (S-01): a no-op unless POLYDB_USABILITY=1. It lives next
	// to connections.json so a user can read or delete it; never uploaded.
	if um := usability.Open("fynequery", filepath.Join(configDir(), "usability.ndjson")); um != nil {
		db.UseMetrics(um)
		defer um.Close()
	}
	// SSH host keys live next to connections.json so the portable override
	// (FYNEQUERY_CONFIG_DIR) keeps every stored secret in one place.
	db.SetKnownHostsPath(filepath.Join(configDir(), "ssh-known-hosts.json"))

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
		// The dial runs in a goroutine: nothing can render before a.Run(),
		// and a synchronous dial here meant a blank launch for as long as
		// the dial took. fyne.Do from before Run queues the UI work and it
		// drains once the loop is up (glfw runOnMainWithWait).
		launchDirect := func() {
			// safeGo, not bare go: dialConn panics happen (wave 27 evidence),
			// and an unrecovered panic here takes down the whole process
			// instead of falling back to the welcome page.
			safeGo("directConn", func() {
				conns, _ := loadConnections()
				if len(conns) == 0 {
					safeDo(showWelcome)
					return
				}
				c := conns[0]
				sess, err := dialConn(c)
				safeDo(func() {
					if err != nil {
						fmt.Fprintf(os.Stderr, "open %q: %v\n", c.Name, err)
						showWelcome()
						return
					}
					openWorkspace(a, c, sess, darkMode, showWelcome)
				})
			})
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
