package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestBuildWordsAndMatch(t *testing.T) {
	words := buildWords([]string{"users", "Users"}, []string{"id", "name"})
	count := 0
	for _, w := range words {
		if strings.EqualFold(w, "users") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("users appears %d times, want 1", count)
	}
	if got := matchWords(words, "sel", 10); len(got) == 0 || got[0] != "SELECT" {
		t.Fatalf("matchWords(sel) = %v", got)
	}
	if got := matchWords(words, "xyz", 10); len(got) != 0 {
		t.Fatalf("matchWords(xyz) = %v, want none", got)
	}
	if got := matchWords(words, "", 3); len(got) != 3 {
		t.Fatalf("limit not respected: got %d", len(got))
	}
	for i := 1; i < len(words); i++ {
		if strings.ToLower(words[i-1]) > strings.ToLower(words[i]) {
			t.Fatalf("not sorted at %d: %q > %q", i, words[i-1], words[i])
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
