package main

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestBuildWordsAndMatch(t *testing.T) {
	sugs := buildSuggestions([]string{"users", "Users"}, map[string][]string{"users": {"id", "name"}}, "shop", nil)
	count := 0
	for _, s := range sugs {
		if strings.EqualFold(s.label, "users") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("users appears %d times, want 1", count)
	}
	if got := matchSuggestions(sugs, "sel", 10); len(got) == 0 || got[0].label != "SELECT" {
		t.Fatalf("matchSuggestions(sel) = %v", got)
	}
	if got := matchSuggestions(sugs, "xyz", 10); len(got) != 0 {
		t.Fatalf("matchSuggestions(xyz) = %v, want none", got)
	}
	if got := matchSuggestions(sugs, "", 3); len(got) != 3 {
		t.Fatalf("limit not respected: got %d", len(got))
	}
	for i := 1; i < len(sugs); i++ {
		if strings.ToLower(sugs[i-1].label) > strings.ToLower(sugs[i].label) {
			t.Fatalf("not sorted at %d: %q > %q", i, sugs[i-1].label, sugs[i].label)
		}
	}
}

func TestRuneOffsetAndWord(t *testing.T) {
	r := []rune("SELECT *\nFROM users\n")
	off := runeOffset(r, 1, 5)
	if string(r[:off]) != "SELECT *\nFROM " {
		t.Fatalf("offset %d -> %q", off, string(r[:off]))
	}
	if runeOffset(r, 99, 0) != len(r) {
		t.Fatal("row clamp failed")
	}

	qp := &queryPane{editor: widget.NewMultiLineEntry()}
	qp.editor.SetText("SELECT coun FROM t")
	qp.setCursorRune(11) // just after "coun"
	start, word := qp.currentWord()
	if word != "coun" {
		t.Fatalf("word = %q, want coun", word)
	}
	if got := string([]rune(qp.editor.Text)[start : start+len(word)]); got != "coun" {
		t.Fatalf("word slice = %q", got)
	}
}

func TestInsertCompletion(t *testing.T) {
	test.NewApp()
	qp := &queryPane{editor: widget.NewMultiLineEntry()}
	qp.editor.SetText("SELECT coun FROM t")
	qp.insertCompletion(7, len([]rune("coun")), "COUNT")
	if qp.editor.Text != "SELECT COUNT FROM t" {
		t.Fatalf("text = %q", qp.editor.Text)
	}
	if qp.editor.CursorRow != 0 || qp.editor.CursorColumn != 12 {
		t.Fatalf("cursor = %d,%d, want 0,12", qp.editor.CursorRow, qp.editor.CursorColumn)
	}
}

func TestReferencedTables(t *testing.T) {
	known := []string{"users", "orders"}
	got := referencedTables("SELECT * FROM users u JOIN orders o ON u.id=o.uid", known)
	if len(got) != 2 {
		t.Fatalf("refs = %v, want users+orders", got)
	}
	if r := referencedTables("SELECT 1", known); len(r) != 0 {
		t.Fatalf("refs = %v, want none", r)
	}
}

func TestSplitDottedWord(t *testing.T) {
	cases := []struct {
		in, seg string
		dot     int
	}{
		{"coun", "coun", 0},
		{"u.na", "na", 2},
		{"public.users.id", "id", 13},
		{"", "", 0},
	}
	for _, c := range cases {
		seg, dot := splitDottedWord(c.in)
		if seg != c.seg || dot != c.dot {
			t.Errorf("splitDottedWord(%q) = (%q,%d), want (%q,%d)", c.in, seg, dot, c.seg, c.dot)
		}
	}
}

func TestAutoPrefixOK(t *testing.T) {
	ok := []string{"s", "sel", "user_name", "_tmp"}
	bad := []string{"", "1", "1abc", "-", "a.b", "a b", "a-b"}
	for _, s := range ok {
		if !autoPrefixOK(s) {
			t.Errorf("autoPrefixOK(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if autoPrefixOK(s) {
			t.Errorf("autoPrefixOK(%q) = true, want false", s)
		}
	}
}

func TestBindEntryShortcut(t *testing.T) {
	test.NewApp()
	e := widget.NewMultiLineEntry()
	called := false
	sc := &desktop.CustomShortcut{KeyName: fyne.KeyReturn, Modifier: fyne.KeyModifierControl}
	if !bindEntryShortcut(e, sc, func(fyne.Shortcut) { called = true }) {
		t.Fatal("bindEntryShortcut failed — Fyne Entry internals changed?")
	}
	// The driver routes the key to the focused entry's TypedShortcut.
	e.TypedShortcut(sc)
	if !called {
		t.Fatal("registered entry shortcut was not invoked")
	}
	// Unknown shortcuts must still reach the Entry's normal handling (no panic).
	e.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierControl})
}

// TestAutoCompleteKeyboardSelection pins Up/Down navigation of the inline
// suggestion list and Enter accepting the highlighted entry.
func TestAutoCompleteKeyboardSelection(t *testing.T) {
	test.NewApp()
	ed := widget.NewMultiLineEntry()
	qp := &queryPane{editor: ed}
	w := test.NewWindow(ed)
	defer w.Close()
	qp.window = w
	w.Canvas().Focus(ed)
	ed.SetText("se")
	qp.setCursorRune(2)

	qp.showAutoSuggestions(0, 2, []suggestion{
		{label: "SELECT", kind: "keyword", owner: "keyword"},
		{label: "SET", kind: "keyword", owner: "keyword"},
	})
	if qp.autoSel != -1 {
		t.Fatalf("initial selection = %d, want none", qp.autoSel)
	}
	proxy := w.Canvas().Focused().(*editorFocusProxy)
	proxy.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	if qp.autoSel != 0 {
		t.Fatalf("down from none selected %d, want 0", qp.autoSel)
	}
	proxy.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	if qp.autoSel != 1 {
		t.Fatalf("second down selected %d, want 1", qp.autoSel)
	}
	proxy.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown}) // clamps at the end
	if qp.autoSel != 1 {
		t.Fatalf("selection ran past the last row: %d", qp.autoSel)
	}
	proxy.TypedKey(&fyne.KeyEvent{Name: fyne.KeyUp})
	if qp.autoSel != 0 {
		t.Fatalf("up selected %d, want 0", qp.autoSel)
	}
	// Enter accepts the highlighted suggestion instead of inserting a newline.
	proxy.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if ed.Text != "SELECT" {
		t.Fatalf("accepted text = %q, want SELECT", ed.Text)
	}
	if qp.autoWords != nil {
		t.Fatal("popup still open after accepting a suggestion")
	}
}

func TestAutoCompleteShowsSuggestions(t *testing.T) {
	test.NewApp()
	ed := widget.NewMultiLineEntry()
	qp := &queryPane{editor: ed}
	w := test.NewWindow(ed)
	defer w.Close()
	qp.window = w
	w.Canvas().Focus(ed)
	ed.SetText("sel")
	qp.setCursorRune(3)
	qp.autoGen++

	// The suggestion lookup is asynchronous (safeGo → safeDo). Wait for it to
	// settle via the test seam instead of polling qp.autoWords: the UI callback
	// writes that field from its own goroutine, so polling it from the test
	// goroutine is a data race (the fyne test driver has no UI thread to
	// serialize against).
	settled := make(chan struct{})
	autoCompleteSettled = func() { close(settled) }
	defer func() { autoCompleteSettled = nil }()

	qp.autoComplete()
	select {
	case <-settled:
	case <-time.After(2 * time.Second):
		t.Fatal("autocomplete did not settle")
	}
	if len(qp.autoWords) == 0 {
		t.Fatal("no inline suggestions for prefix \"sel\"")
	}
	found := false
	for _, s := range qp.autoWords {
		if s == "SELECT" {
			found = true
		}
	}
	if !found {
		t.Fatalf("suggestions %v do not contain SELECT", qp.autoWords)
	}
	if !qp.editorFocused() {
		t.Fatalf("popup stole keyboard focus: %T", w.Canvas().Focused())
	}
	proxy, ok := w.Canvas().Focused().(*editorFocusProxy)
	if !ok || proxy.target != fyne.Focusable(ed) {
		t.Fatalf("overlay focus must forward to the editor, got %T", w.Canvas().Focused())
	}
	// Keyboard input routed to the focused object must still reach the editor.
	hit := false
	probe := &desktop.CustomShortcut{KeyName: fyne.KeyF9, Modifier: fyne.KeyModifierControl}
	if !bindEntryShortcut(ed, probe, func(fyne.Shortcut) { hit = true }) {
		t.Fatal("bindEntryShortcut failed")
	}
	w.Canvas().Focused().(fyne.Shortcutable).TypedShortcut(probe)
	if !hit {
		t.Fatal("shortcut did not reach the editor through the popup proxy")
	}
	first := qp.autoWords[0]
	qp.acceptAuto(0)
	if got := strings.TrimSpace(ed.Text); !strings.EqualFold(got, first) {
		t.Fatalf("acceptAuto did not replace the word, text = %q want %q", got, first)
	}
	if got := w.Canvas().Focused(); got != fyne.Focusable(ed) {
		t.Fatalf("focus did not return to the editor after the popup closed: %T", got)
	}
}
