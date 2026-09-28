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
