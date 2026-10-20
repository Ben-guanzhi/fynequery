package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// TestSidebarTreeFillsHeight renders the real main window on the test canvas
// and asserts the navigator tree absorbs the sidebar's leftover height. This
// is the regression test for the "树区太小" report: a VBox wrapper used to
// give the tree only its minimum height.
func TestSidebarTreeFillsHeight(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := &Client{
		app:   a,
		win:   w,
		store: db.NewStore(),
		tree:  treeState{nodes: make(map[string]*nodeData), expanded: make(map[string]bool)},
		cache: memoCache{tables: make(map[string][]string), objects: make(map[string][]string), loading: make(map[string]bool), loadingObjects: make(map[string]bool), fks: make(map[string][]db.ForeignKeyInfo)},
		grid:  gridState{edits: newGridEditState(), sortCol: -1},
	}
	content := c.buildUI()
	w.SetContent(content)
	w.Resize(fyne.NewSize(1200, 900))

	if c.tree.rows == nil {
		t.Fatal("schema rows not built")
	}
	h := c.tree.rows.Size().Height
	// One simulated database row keeps the tree non-empty.
	if h < 200 {
		t.Fatalf("tree viewport height = %.0f, want a tall fill (>=200)", h)
	}
}

// TestEditorReceivesTypedText drives the real canvas focus pipeline: focus the
// editor and type through the canvas, the way the driver does after a click.
func TestEditorReceivesTypedText(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := &Client{
		app:   a,
		win:   w,
		store: db.NewStore(),
		tree:  treeState{nodes: make(map[string]*nodeData), expanded: make(map[string]bool)},
		cache: memoCache{tables: make(map[string][]string), objects: make(map[string][]string), loading: make(map[string]bool), loadingObjects: make(map[string]bool), fks: make(map[string][]db.ForeignKeyInfo)},
		grid:  gridState{edits: newGridEditState(), sortCol: -1},
	}
	w.SetContent(c.buildUI())

	if c.tabs.entry == nil {
		t.Fatal("query entry not built")
	}
	// Mimic the real glfw driver: char input goes to canvas.Focused().
	w.Canvas().SetOnTypedRune(func(r rune) {
		if f := w.Canvas().Focused(); f != nil {
			f.TypedRune(r)
		}
	})
	w.Canvas().Focus(c.tabs.entry)
	if w.Canvas().Focused() == nil {
		t.Fatal("editor did not take focus")
	}
	test.TypeOnCanvas(w.Canvas(), "SELECT")
	if got := c.tabs.entry.Text; got != "SELECT" {
		t.Fatalf("typed text = %q, want SELECT", got)
	}
}

// findContainerWith returns the container whose first child is want, walking
// through widget renderers as well because the main layout nests the query page
// inside a *container.Split, which is a widget, not a *fyne.Container.
func findContainerWith(objs []fyne.CanvasObject, want fyne.CanvasObject) *fyne.Container {
	for _, o := range objs {
		switch n := o.(type) {
		case *fyne.Container:
			if len(n.Objects) > 0 && n.Objects[0] == want {
				return n
			}
			if r := findContainerWith(n.Objects, want); r != nil {
				return r
			}
		case fyne.Widget:
			rend := test.WidgetRenderer(n)
			if rend == nil {
				continue
			}
			if r := findContainerWith(rend.Objects(), want); r != nil {
				return r
			}
		}
	}
	return nil
}

// TestToolbarFitsMainPane pins the overflow fix. An HBox never wraps: it lays
// every child at its own MinSize width and simply pushes the tail past the
// right edge, where it is clipped and unreachable. With the default window the
// query toolbar used to need 1023px against ~990px of pane, so 导出 and the
// theme select were cut off.
func TestToolbarFitsMainPane(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	root := c.buildUI()
	w.SetContent(root)
	w.Resize(fyne.NewSize(1280, 800))

	bar := findContainerWith([]fyne.CanvasObject{root}, c.dbs.sel)
	if bar == nil {
		t.Fatal("toolbar not found")
	}
	last := bar.Objects[len(bar.Objects)-1]
	// A layout pass has to have run for positions to mean anything.
	if last.Position().X == 0 {
		t.Fatal("toolbar was never laid out")
	}
	if right := last.Position().X + last.MinSize().Width; right > bar.Size().Width {
		t.Fatalf("toolbar tail clipped: last control ends at %.0f, bar is %.0f wide (needs %.0f)",
			right, bar.Size().Width, bar.MinSize().Width)
	}
	if need := bar.MinSize().Width; need > bar.Size().Width {
		t.Fatalf("toolbar needs %.0fpx, pane gives %.0fpx", need, bar.Size().Width)
	}
}

// TestKeyDialogDocumentsEveryBinding keeps the 快捷键 dialog honest: it is the
// only place the combos are spelled out now that the hint label is gone, so a
// binding without a doc line (or a doc line that repeats on a second layout) is
// a bug.
func TestKeyDialogDocumentsEveryBinding(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildUI())

	text := strings.Join(c.keyDocs, "\n")
	documented := map[string]bool{}
	for combo, b := range c.keys {
		if b.desc == "" {
			continue
		}
		name := keyName(combo.key)
		documented[name] = true
		if !strings.Contains(text, "+ "+name+" ") {
			t.Fatalf("combo %s (%q) is bound but not documented: %q", combo.key, b.desc, text)
		}
	}
	if len(documented) < 5 {
		t.Fatalf("only %d distinct documented combos, expected at least 5", len(documented))
	}
	// The numpad Enter shares the run action and must not add a second line.
	if n := strings.Count(text, "执行编辑器中的 SQL"); n != 1 {
		t.Fatalf("run action documented %d times, want 1", n)
	}

	// One line per documented combo plus the combos written by hand: Ctrl+C
	// (canvas-level only), the search box's Enter / Shift+Enter, the editor's
	// Tab (indent, claimed in the editor's TypedKey not the shortcut table),
	// and the Ctrl+1…9 tab run, which nine table entries share instead of
	// documenting nine near-identical lines. The dialog is the only place the
	// keys are still spelled out, so a stale or missing line is a real defect.
	if want := len(documented) + 4; len(c.keyDocs) != want {
		t.Fatalf("dialog has %d lines, want %d: %q", len(c.keyDocs), want, c.keyDocs)
	}
	if !strings.Contains(text, "1 … 9") {
		t.Fatalf("the tab-number run is not documented: %q", text)
	}
}
