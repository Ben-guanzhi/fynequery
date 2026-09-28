package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// newKeyClient builds the whole UI and replaces every binding in the shortcut
// table with a recorder, so a test can see which combo claimed the keystroke
// without running the real action (dialogs, queries, tab churn).
func newKeyClient(t *testing.T) (*Client, *[]fyne.KeyName) {
	t.Helper()
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildUI())

	fired := &[]fyne.KeyName{}
	for combo, b := range c.keys {
		key := combo.key
		b.fn = func() { *fired = append(*fired, key) }
		c.keys[combo] = b
	}
	return c, fired
}

// TestShortcutsClaimedByEveryEntry pins the fix: glfw delivers a shortcut to
// the focused widget and never to the canvas when that widget is
// Shortcutable, so each entry has to consult the table itself. Before this,
// Ctrl+P / Ctrl+N (and the rest) were dead whenever the editor or a filter box
// had focus — which is most of the time.
func TestShortcutsClaimedByEveryEntry(t *testing.T) {
	c, fired := newKeyClient(t)

	targets := []struct {
		name string
		sc   fyne.Shortcutable
	}{
		{"SQL editor", c.tabs.entry},
		{"result search", c.find.entry},
		{"cell viewer", c.grid.view},
		{"tree filter", c.tree.filterEntry},
		{"history filter", c.hist.filterEntry},
	}
	keys := []fyne.KeyName{fyne.KeyP, fyne.KeyN, fyne.KeyReturn, fyne.KeyEnter, fyne.KeyO, fyne.KeyS}
	mods := []fyne.KeyModifier{fyne.KeyModifierControl, fyne.KeyModifierSuper}

	for _, tg := range targets {
		for _, key := range keys {
			for _, mod := range mods {
				*fired = nil
				tg.sc.TypedShortcut(&desktop.CustomShortcut{KeyName: key, Modifier: mod})
				if len(*fired) != 1 || (*fired)[0] != key {
					t.Fatalf("%s: %v+%s fired %v, want exactly [%s]", tg.name, mod, key, *fired, key)
				}
			}
		}
	}

	// Ctrl+Enter must not leak into the editor as a newline.
	c.tabs.entry.setTextSilently("SELECT 1")
	*fired = nil
	c.tabs.entry.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyReturn, Modifier: fyne.KeyModifierControl})
	if c.tabs.entry.Text != "SELECT 1" {
		t.Fatalf("editor text after Ctrl+Enter = %q, want unchanged", c.tabs.entry.Text)
	}
}

// TestShortcutsFallThroughToEntry checks the other half: combos the table does
// not own keep their normal text-editing behaviour.
func TestShortcutsFallThroughToEntry(t *testing.T) {
	c, fired := newKeyClient(t)
	c.tabs.entry.setTextSilently("SELECT 1")

	// A different modifier set is a different combo: Ctrl+Shift+S saves nothing.
	c.tabs.entry.TypedShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyS,
		Modifier: fyne.KeyModifierControl | fyne.KeyModifierShift,
	})
	if len(*fired) != 0 {
		t.Fatalf("Ctrl+Shift+S fired %v, want nothing", *fired)
	}

	// Copy stays with the entry so a selection inside a box can be copied.
	if _, ok := c.keys[keyCombo{fyne.KeyC, fyne.KeyModifierControl}]; ok {
		t.Fatal("Ctrl+C must not be claimed by the app table")
	}

	// Entry's own shortcuts still work: select-all marks the whole text.
	c.tabs.entry.TypedShortcut(&fyne.ShortcutSelectAll{})
	if got := c.tabs.entry.SelectedText(); got != "SELECT 1" {
		t.Fatalf("select-all left %q, want the full text", got)
	}
}

// TestKeysDialogButtonOpensDialog wires the toolbar button to the dialog: the
// hint label it replaced was the only place the combos were written down, so
// tapping 快捷键 has to actually show something.
func TestKeysDialogButtonOpensDialog(t *testing.T) {
	c, _ := newKeyClient(t)

	var btn *widget.Button
	for _, o := range test.LaidOutObjects(c.win.Content()) {
		if b, ok := o.(*widget.Button); ok && b.Text == "快捷键" {
			btn = b
			break
		}
	}
	if btn == nil {
		t.Fatal("no 快捷键 button in the laid-out UI")
	}
	if before := len(c.win.Canvas().Overlays().List()); before != 0 {
		t.Fatalf("%d overlays already open before the tap", before)
	}
	test.Tap(btn)
	if len(c.win.Canvas().Overlays().List()) == 0 {
		t.Fatal("tapping 快捷键 opened no dialog")
	}
}
