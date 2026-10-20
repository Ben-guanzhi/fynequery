package main

// These tests pin what Ctrl+Enter means once a tab can hold more than one
// statement. They drive the real thing - a SQLite connection and the built UI -
// because the answer is recorded in the statement history: "which statement did
// that keystroke actually run" is exactly what the log says, and nothing else
// in the app reports it.

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// threeStatements is a script whose every line is independently runnable, so
// "which of them ran" has one possible answer each time.
const threeStatements = "SELECT COUNT(*) FROM items;\n" +
	"SELECT id FROM items WHERE id = 1;\n" +
	"SELECT name FROM nokey LIMIT 1"

// historySQL is what the three statements' executions recorded, oldest first.
func historySQL(c *Client) []string {
	out := make([]string, 0, 4)
	for _, e := range c.store.History() {
		out = append(out, e.SQL)
	}
	return out
}

// TestCtrlEnterRunsOnlyTheStatementUnderTheCaret is the whole point of the
// change: three statements in one tab, the caret in the middle one, and only
// that one reaches the server. Before this Ctrl+Enter ran all three, which is
// why nobody kept three statements in one tab.
func TestCtrlEnterRunsOnlyTheStatementUnderTheCaret(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	c.tabs.entry.setTextSilently(threeStatements)
	c.tabs.entry.CursorRow, c.tabs.entry.CursorColumn = 1, 3 // inside statement 2

	c.runCurrent()

	ran := historySQL(c)
	if len(ran) != 1 {
		t.Fatalf("history = %v, want exactly one statement to have run", ran)
	}
	if ran[0] != "SELECT id FROM items WHERE id = 1" {
		t.Errorf("ran %q, want the statement the caret sits in", ran[0])
	}
	if !strings.Contains(c.status.Text, "执行的是第 2 条") {
		t.Errorf("status = %q, does not say which statement ran", c.status.Text)
	}
	if strings.Contains(c.status.Text, "光标所在语句") {
		t.Errorf("status = %q, the busy wording leaked into the result line", c.status.Text)
	}
}

// TestCtrlShiftEnterStillRunsTheWholeTab keeps the old behaviour reachable: a
// script that has to run as a whole still has to run as a whole.
func TestCtrlShiftEnterStillRunsTheWholeTab(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	c.tabs.entry.setTextSilently(threeStatements)
	c.tabs.entry.CursorRow, c.tabs.entry.CursorColumn = 1, 3 // ignored by this path

	c.runAllStatements()

	if ran := historySQL(c); len(ran) != 3 {
		t.Fatalf("history = %v, want all three statements", ran)
	}
}

// TestBothRunCombosAreReachableThroughTheKeyTable pins the keystrokes rather
// than the functions behind them: the whole point is that the editor keeps the
// keyboard, so an Entry claiming the combo is exactly the bug this guards.
func TestBothRunCombosAreReachableThroughTheKeyTable(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	entry := c.tabs.entry
	entry.setTextSilently(threeStatements)
	entry.CursorRow, entry.CursorColumn = 1, 3

	entry.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyReturn, Modifier: fyne.KeyModifierControl})
	if ran := historySQL(c); len(ran) != 1 {
		t.Fatalf("Ctrl+Enter ran %d statements, want the one under the caret", len(ran))
	}

	c.store.ReplaceHistory(nil)
	entry.TypedShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyReturn,
		Modifier: fyne.KeyModifierControl | fyne.KeyModifierShift,
	})
	if ran := historySQL(c); len(ran) != 3 {
		t.Fatalf("Ctrl+Shift+Enter ran %d statements, want all three", len(ran))
	}
}

// TestAPartialRunDoesNotClearTheDirtyMarker: ● means "changed since you last
// ran IT", so running one of three statements cannot clear it - the other two
// are still waiting.
func TestAPartialRunDoesNotClearTheDirtyMarker(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	c.tabs.entry.SetText(threeStatements) // through OnChanged, so the tab goes dirty
	tab := c.tabs.items[0]
	if !tab.dirty || !strings.HasSuffix(tab.item.Text, "●") {
		t.Fatalf("tab %q is not marked dirty after typing", tab.item.Text)
	}

	c.tabs.entry.CursorRow, c.tabs.entry.CursorColumn = 1, 3
	c.runCurrent()
	if !tab.dirty {
		t.Fatal("a partial run cleared ●; the un-run statements are still unexecuted")
	}

	c.runAllStatements()
	if tab.dirty || strings.HasSuffix(tab.item.Text, "●") {
		t.Fatalf("tab %q kept ● after the whole tab ran", tab.item.Text)
	}
}

// TestRunRefuses a caret that sits where there is nothing to run - the empty
// run after a semicolon, or a tab of nothing but comments.
func TestARunWithNothingUnderTheCaretSaysSo(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	c.tabs.entry.setTextSilently("-- nothing here yet\n/* nor here */")

	c.runCurrent()

	if ran := historySQL(c); len(ran) != 0 {
		t.Fatalf("history = %v, a comment-only tab must run nothing", ran)
	}
	if c.status.Text == "" || !strings.Contains(c.status.Text, "没有可执行的语句") {
		t.Errorf("status = %q, want the reason nothing ran", c.status.Text)
	}
}

// TestSelectingTheWholeEditorRunsEveryStatement exercises the selection path
// end to end: fyne hands over the selected TEXT but not where it starts, so
// recovering the range from the caret is the part that could be wrong.
func TestSelectingTheWholeEditorRunsEveryStatement(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	c.tabs.entry.setTextSilently(threeStatements)
	c.tabs.entry.TypedShortcut(&fyne.ShortcutSelectAll{})
	if sel := c.tabs.entry.SelectedText(); sel != threeStatements {
		t.Fatalf("SelectedText = %q, want the whole tab selected", sel)
	}

	c.runCurrent()

	if ran := historySQL(c); len(ran) != 3 {
		t.Fatalf("history = %v, want a whole-tab selection to run all three", ran)
	}
}

// TestSelectionRange covers the recovery itself, including the case that can
// genuinely be ambiguous: the same text twice, where looking backwards and
// looking forwards both match. It must pick one and say it picked one, never
// silently run the wrong end of the tab.
func TestSelectionRange(t *testing.T) {
	cases := []struct {
		name      string
		text, sel string
		caret     int
		wantStart int
		wantEnd   int
		wantOK    bool
	}{
		{"caret at the end of the selection", "ab CD ef", "CD", 5, 3, 5, true},
		{"caret at the start of the selection", "ab CD ef", "CD", 3, 3, 5, true},
		{"whole text selected", "SELECT 1", "SELECT 1", 8, 0, 8, true},
		{"empty selection never resolves", "SELECT 1", "", 3, 0, 0, false},
		{"caret outside the text never resolves", "ab", "ab", -1, 0, 0, false},
		{"a repeated word resolves to where the caret says", "x x", "x", 2, 2, 3, true},
	}
	for _, tc := range cases {
		start, end, ok := selectionRange(tc.text, tc.sel, tc.caret)
		if ok != tc.wantOK || start != tc.wantStart || end != tc.wantEnd {
			t.Errorf("%s: selectionRange = (%d,%d,%v), want (%d,%d,%v)",
				tc.name, start, end, ok, tc.wantStart, tc.wantEnd, tc.wantOK)
			continue
		}
		if ok && tc.text[start:end] != tc.sel {
			t.Errorf("%s: resolved range does not hold the selected text: %q",
				tc.name, tc.text[start:end])
		}
	}
}
