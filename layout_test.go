package main

import (
	"fmt"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

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

// TestToolbarFitsMainPane pins the overflow fix. The toolbar is a Border, not
// an HBox: the right group (limit, paging, export, theme) is pinned to the
// right edge instead of being pushed past it, so it can never be clipped the
// way an HBox tail would be. We assert two things - the toolbar's own MinSize
// fits the pane, and the right-most control (the theme select) actually lands
// inside the toolbar's right edge after layout.
func TestToolbarFitsMainPane(t *testing.T) {
	for _, width := range []float32{1280, 1024} {
		t.Run(fmt.Sprintf("w%.0f", width), func(t *testing.T) {
			a := test.NewApp()
			w := a.NewWindow("t")
			c := newTestClient(a, w)
			root := c.buildUI()
			w.SetContent(root)
			w.Resize(fyne.NewSize(width, 800))

			bar := c.queryToolbar
			if bar == nil {
				t.Fatal("toolbar not found")
			}
			// A layout pass has to have run for positions to mean anything.
			if bar.Size().Width == 0 {
				t.Fatal("toolbar was never laid out")
			}
			if need := bar.MinSize().Width; need > bar.Size().Width {
				t.Fatalf("toolbar needs %.0fpx, pane gives %.0fpx", need, bar.Size().Width)
			}
			// The theme select is the rightmost control; find it by its
			// placeholder and confirm it stays inside the toolbar.
			var themeSel *widget.Select
			for _, o := range test.LaidOutObjects(bar) {
				if s, ok := o.(*widget.Select); ok && s.PlaceHolder == "主题" {
					themeSel = s
					break
				}
			}
			if themeSel == nil {
				t.Fatal("theme select not found in toolbar")
			}
			if right := themeSel.Position().X + themeSel.MinSize().Width; right > bar.Size().Width {
				t.Fatalf("rightmost control ends at %.0f, toolbar is %.0f wide",
					right, bar.Size().Width)
			}
		})
	}
}

// TestTabStripAndActionsSameRow pins the tab-strip redesign: the new/close/open/
// save buttons used to sit on a row of their own above the tabs, costing the
// editor ~40px. Now they share the tab strip's row on its right. We assert the
// action group and the tab bar land on the same top edge after layout - if they
// drift apart, the Border that nests them has broken and the buttons are back
// on their own row.
func TestTabStripAndActionsSameRow(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildUI())
	w.Resize(fyne.NewSize(1280, 800))

	if c.tabs.actions == nil || c.tabs.bar == nil {
		t.Fatal("tab strip or action group was not built")
	}
	barY := c.tabs.bar.Position().Y
	actY := c.tabs.actions.Position().Y
	if barY != actY {
		t.Fatalf("action group top (%.0f) != tab bar top (%.0f): buttons are no longer on the tab row",
			actY, barY)
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
	// Docs, not table entries: every combo is bound twice (Ctrl and Cmd) and
	// one doc line covers both, so counting entries would want twice the lines
	// the dialog actually has.
	documented := map[string]bool{}
	docs := map[string]bool{}
	for combo, b := range c.keys {
		if b.desc == "" {
			continue
		}
		name := keyName(combo.key)
		documented[name] = true
		docs[b.desc] = true
		if !strings.Contains(text, "+ "+name+" ") {
			t.Fatalf("combo %s (%q) is bound but not documented: %q", combo.key, b.desc, text)
		}
	}
	if len(documented) < 5 {
		t.Fatalf("only %d distinct documented combos, expected at least 5", len(documented))
	}
	// The numpad Enter shares the run action and must not add a second line -
	// same for its Shift flavour, which documents one combo, not one per key.
	for _, line := range []string{"执行光标所在的那条语句", "执行编辑器中的全部语句"} {
		if n := strings.Count(text, line); n != 1 {
			t.Fatalf("%q documented %d times, want 1", line, n)
		}
	}

	// One line per documented combo plus the combos written by hand: Ctrl+C
	// (canvas-level only), the search box's Enter / Shift+Enter, the editor's
	// Tab (indent, claimed in the editor's TypedKey not the shortcut table),
	// the Ctrl+1…9 tab run (nine table entries share one line), and the two
	// grid undo/redo combos (canvas-level only, so the editor keeps its own
	// text undo). The dialog is the only place the keys are still spelled out,
	// so a stale or missing line is a real defect.
	if want := len(docs) + 6; len(c.keyDocs) != want {
		t.Fatalf("dialog has %d lines, want %d: %q", len(c.keyDocs), want, c.keyDocs)
	}
	if !strings.Contains(text, "1 … 9") {
		t.Fatalf("the tab-number run is not documented: %q", text)
	}
}
