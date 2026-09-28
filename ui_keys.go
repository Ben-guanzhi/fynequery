package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// App-level keyboard shortcuts, kept in one table.
//
// The glfw driver delivers a shortcut to the FOCUSED widget when that widget is
// fyne.Shortcutable — every Entry is — and then never consults the canvas's
// registered shortcuts. Registering on the canvas alone therefore leaves
// Ctrl+P / Ctrl+N / Ctrl+S dead exactly when the SQL editor or a filter box has
// focus, which is most of the time. So every entry that can hold focus asks
// this table first (see entryShortcut), while the canvas registrations stay as
// the fallback for all other focus targets.

// keyCombo identifies one binding: a key plus a single modifier. Ctrl+Shift+S
// is a different combo and is deliberately not claimed here.
type keyCombo struct {
	key fyne.KeyName
	mod fyne.KeyModifier
}

// keyBinding is what a combo does, plus the one-line description the 快捷键
// dialog shows. Documenting at the binding site is what keeps the dialog from
// rotting when a combo is added or moved.
type keyBinding struct {
	fn   func()
	desc string
}

// keyName prints a fyne key as the label used in the dialog. The letter keys
// already spell themselves ("P"), so only the two Enter flavours need a name.
func keyName(key fyne.KeyName) string {
	switch key {
	case fyne.KeyReturn:
		return "Enter"
	case fyne.KeyEnter:
		return "小键盘 Enter"
	default:
		return string(key)
	}
}

// bindKey registers an action for Ctrl and Cmd on all focus targets. An empty
// desc keeps the binding out of the 快捷键 dialog, which is right for the
// alternate spelling of a key already documented (numpad Enter).
func (c *Client) bindKey(key fyne.KeyName, desc string, fn func()) {
	if c.keys == nil {
		c.keys = make(map[keyCombo]keyBinding)
	}
	for _, mod := range []fyne.KeyModifier{fyne.KeyModifierControl, fyne.KeyModifierSuper} {
		c.keys[keyCombo{key, mod}] = keyBinding{fn: fn, desc: desc}
		sc := &desktop.CustomShortcut{KeyName: key, Modifier: mod}
		c.win.Canvas().AddShortcut(sc, func(fyne.Shortcut) { fn() })
	}
	if desc != "" {
		c.docKey(fmt.Sprintf("Ctrl / Cmd + %s    %s", keyName(key), desc))
	}
}

// docKey adds a line to the 快捷键 dialog for a combo that is registered
// directly on the canvas rather than through bindKey.
func (c *Client) docKey(line string) {
	c.keyDocs = append(c.keyDocs, line)
}

// showKeysDialog lists every documented combo. It exists because the toolbar
// hint label it replaces needed ~300px, which was enough to push the export and
// theme controls past the edge of the main pane.
func (c *Client) showKeysDialog() {
	dialog.ShowInformation("快捷键", strings.Join(c.keyDocs, "\n"), c.win)
}

// entryShortcut lets a focused entry claim a combo before its own
// text-editing shortcuts; it reports whether the combo was handled.
func (c *Client) entryShortcut(sc fyne.Shortcut) bool {
	cs, ok := sc.(*desktop.CustomShortcut)
	if !ok {
		return false
	}
	b, ok := c.keys[keyCombo{cs.KeyName, cs.Modifier}]
	if !ok {
		return false
	}
	b.fn()
	return true
}

// keyedEntry is a one-line entry that consults the app shortcut table first, so
// the global combos keep working while a filter or preview box has focus.
type keyedEntry struct {
	widget.Entry
	onShortcut func(fyne.Shortcut) bool
}

func newKeyedEntry() *keyedEntry {
	e := &keyedEntry{}
	e.ExtendBaseWidget(e)
	return e
}

func (e *keyedEntry) TypedShortcut(sc fyne.Shortcut) {
	if e.onShortcut != nil && e.onShortcut(sc) {
		return
	}
	e.Entry.TypedShortcut(sc)
}
