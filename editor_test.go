package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

func newTestEntry() *suggestEntry {
	test.NewApp()
	return newSuggestEntry()
}

func TestMatchSuggestions(t *testing.T) {
	pool := []string{"SELECT", "SET", "users", "USER_ROLES", "sessions", "SELECT", ""}
	got := matchSuggestions(pool, "se")
	// Sort is bytewise: capitals sort before lowercase.
	want := []string{"SELECT", "SET", "sessions"}
	if len(got) != len(want) {
		t.Fatalf("matchSuggestions(se) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("matchSuggestions(se) = %v, want %v", got, want)
		}
	}
	if got := matchSuggestions(pool, "select"); len(got) != 0 {
		t.Fatalf("exact match kept: %v", got)
	}
	// Cap: pool has many "a" words, result must not exceed maxSuggestions.
	big := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"}
	if got := matchSuggestions(big, "a"); len(got) != maxSuggestions {
		t.Fatalf("cap = %d, want %d", len(got), maxSuggestions)
	}
}

func TestWordSplice(t *testing.T) {
	s := newTestEntry()
	s.SetText("SELECT * FR")
	s.CursorRow, s.CursorColumn = 0, 11 // end of "FR"

	w := s.currentWord()
	if w.text != "FR" || w.start != 9 || w.end != 11 {
		t.Fatalf("currentWord = %+v, want text FR span [9,11)", w)
	}
	s.suggest = []string{"FROM"}
	s.acceptSuggestion()
	if got := s.Entry.Text; got != "SELECT * FROM" {
		t.Fatalf("text after accept = %q", got)
	}
	if s.Entry.CursorColumn != 13 {
		t.Fatalf("cursor after accept = %d, want 13", s.Entry.CursorColumn)
	}
	if len(s.suggest) != 0 {
		t.Fatalf("suggestions not cleared after accept")
	}
}

func TestSuggestEntryTyping(t *testing.T) {
	s := newTestEntry()
	test.Type(s, "SEL")
	if s.Entry.Text != "SEL" {
		t.Fatalf("typing through the wrapper produced %q", s.Entry.Text)
	}

	// Candidates open: plain characters still insert (they are runes, not keys).
	s.setSuggestions([]string{"SELECT", "SET"})
	test.Type(s, " *")
	if s.Entry.Text != "SEL *" {
		t.Fatalf("typing with candidates open produced %q", s.Entry.Text)
	}
	if len(s.suggest) != 2 {
		t.Fatalf("candidates lost while typing: %v", s.suggest)
	}

	// Down/Up move the selection instead of moving the caret; Enter accepts.
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	if s.sugSel != 1 {
		t.Fatalf("down key left selection at %d, want 1", s.sugSel)
	}
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyUp})
	if s.sugSel != 0 {
		t.Fatalf("up key left selection at %d, want 0", s.sugSel)
	}
	s.Entry.SetText("SELECT * FR")
	s.Entry.CursorRow, s.Entry.CursorColumn = 0, 11
	s.suggest, s.sugSel = []string{"FROM"}, 0 // what the OnChanged handler would compute
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEnter})
	if s.Entry.Text != "SELECT * FROM" {
		t.Fatalf("enter did not accept the suggestion: %q", s.Entry.Text)
	}

	// Escape dismisses without touching the text.
	s.setSuggestions([]string{"FROM"})
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if len(s.suggest) != 0 || s.Entry.Text != "SELECT * FROM" {
		t.Fatalf("escape changed state: %v %q", s.suggest, s.Entry.Text)
	}
}

func TestSuggestEntryGrowsWithLines(t *testing.T) {
	test.NewApp()
	e := newSuggestEntry()
	e.SetText("line1")
	h1 := e.MinSize().Height
	for i := 2; i <= 10; i++ {
		e.SetText(e.Text + "\n" + "line" + string(rune('0'+i)))
	}
	h10 := e.MinSize().Height
	if h10 <= h1 {
		t.Fatalf("editor MinSize did not grow: %v -> %v", h1, h10)
	}
}

func TestFirstCodeWord(t *testing.T) {
	cases := map[string]string{
		"BEGIN":           "BEGIN",
		"  \tif x = 1;":   "IF",
		"begin":           "BEGIN",
		"FOR":             "FOR",
		"FORM":            "FORM", // whole word only
		"-- IF":           "",
		"/* IF":           "",
		"'IF'":            "",
		"`IF`":            "",
		"   ":             "",
		"":                "",
		"SELECT * FROM t": "SELECT",
		"x := 1; THEN":    "X",
	}
	for line, want := range cases {
		if got := firstCodeWord(line); got != want {
			t.Errorf("firstCodeWord(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestSplitLineForEnter(t *testing.T) {
	cases := []struct {
		text     string
		row, col int
		wantText string
		wantCol  int
		wantOK   bool
	}{
		{"BEGIN", 0, 5, "BEGIN\n  ", 2, true},
		// The line's own indent is carried onto the new line: a trigger word
		// adds a level RELATIVE to it, a non-trigger keeps it verbatim.
		{"  IF x = 1;", 0, 11, "  IF x = 1;\n    ", 4, true},
		{"  BEGIN", 0, 7, "  BEGIN\n    ", 4, true},
		{"    WHERE x = 1", 0, 15, "    WHERE x = 1\n    ", 4, true},
		{"LOOP", 0, 4, "LOOP\n  ", 2, true},
		{"WHILE", 0, 5, "WHILE\n  ", 2, true},
		{"EXCEPTION", 0, 9, "EXCEPTION\n  ", 2, true},
		{"ELSIF", 0, 5, "ELSIF\n  ", 2, true},
		// Not a trigger word: plain newline, caret at column 0.
		{"SELECT * FROM t", 0, 15, "SELECT * FROM t\n", 0, true},
		{"FORM", 0, 4, "FORM\n", 0, true},
		{"-- IF", 0, 5, "-- IF\n", 0, true},
		// Enter still splits a mid-line caret (columns are rune indices).
		{"SELECT * FROM t", 0, 8, "SELECT *\n FROM t", 0, true},
		{"BEGIN", 0, 4, "BEGI\n  N", 2, true},
		// Other lines are carried over untouched.
		{"a\nBEGIN\nc", 1, 5, "a\nBEGIN\n  \nc", 2, true},
		{"BEGIN\nSELECT 1", 0, 5, "BEGIN\n  \nSELECT 1", 2, true},
		{"BEGIN", 0, 0, "\n  BEGIN", 2, true},
		{"BEGIN", 0, 5, "BEGIN\n  ", 2, true},
		// Out of range: leave the key to its default handling.
		{"BEGIN", -1, 0, "", 0, false},
		{"BEGIN", 1, 0, "", 0, false},
		{"BEGIN", 0, -1, "", 0, false},
		{"BEGIN", 0, 6, "", 0, false},
	}
	for _, c := range cases {
		text, col, ok := splitLineForEnter(c.text, c.row, c.col)
		if ok != c.wantOK || text != c.wantText || col != c.wantCol {
			t.Errorf("splitLineForEnter(%q,%d,%d) = (%q,%d,%v), want (%q,%d,%v)",
				c.text, c.row, c.col, text, col, ok, c.wantText, c.wantCol, c.wantOK)
		}
	}
}

func TestSmartIndentEnter(t *testing.T) {
	s := newTestEntry()
	s.SetText("BEGIN")
	s.CursorRow, s.CursorColumn = 0, 5
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEnter})
	if s.Entry.Text != "BEGIN\n  " {
		t.Fatalf("smart indent enter = %q", s.Entry.Text)
	}
	if s.CursorRow != 1 || s.CursorColumn != 2 {
		t.Fatalf("cursor = %d:%d, want 1:2", s.CursorRow, s.CursorColumn)
	}

	// Return is Enter; a non-trigger line gets the plain newline.
	s.Entry.SetText("SELECT 1")
	s.Entry.CursorRow, s.Entry.CursorColumn = 0, 8
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if s.Entry.Text != "SELECT 1\n" || s.CursorRow != 1 || s.CursorColumn != 0 {
		t.Fatalf("plain enter = %q at %d:%d", s.Entry.Text, s.CursorRow, s.CursorColumn)
	}

	// An open candidate bar still wins over smart indent.
	s.Entry.SetText("SELECT * FR")
	s.Entry.CursorRow, s.Entry.CursorColumn = 0, 11
	s.suggest, s.sugSel = []string{"FROM"}, 0
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEnter})
	if s.Entry.Text != "SELECT * FROM" {
		t.Fatalf("enter accepted nothing: %q", s.Entry.Text)
	}
}

// TestIndentTab pins that Tab inside the editor indents the caret instead of
// tabbing focus away: two spaces land at the caret and the caret advances
// past them. An out-of-range caret leaves the key to its default meaning.
func TestIndentTab(t *testing.T) {
	s := newTestEntry()
	s.Entry.SetText("SELECT 1")
	s.Entry.CursorRow, s.Entry.CursorColumn = 0, 2
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyTab})
	if s.Entry.Text != "SE  LECT 1" {
		t.Fatalf("tab = %q, want two spaces at the caret", s.Entry.Text)
	}
	if s.Entry.CursorRow != 0 || s.Entry.CursorColumn != 4 {
		t.Fatalf("cursor = %d:%d, want 0:4", s.Entry.CursorRow, s.Entry.CursorColumn)
	}

	// A caret past the end of the text is not inside it, so Tab must not be
	// claimed and the text must stay put.
	s2 := newTestEntry()
	s2.Entry.SetText("abc")
	s2.Entry.CursorRow, s2.Entry.CursorColumn = 5, 0
	if s2.indentTab() {
		t.Fatal("an out-of-range caret must not be indented")
	}
	if s2.Entry.Text != "abc" {
		t.Fatalf("text changed on an out-of-range tab: %q", s2.Entry.Text)
	}
}
