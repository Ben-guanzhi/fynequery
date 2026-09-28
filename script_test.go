package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
)

// TestScriptLoadSave pins the transfer helpers behind the file dialogs: load
// fills the active tab (and marks it modified), save writes LF-normalised text.
func TestScriptLoadSave(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildQueryView())

	dir := t.TempDir()
	src := filepath.Join(dir, "load.sql")
	if err := os.WriteFile(src, []byte("SELECT 1;\r\nSELECT 2;\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := c.loadScript(f); err != nil {
		t.Fatalf("loadScript: %v", err)
	}
	if got := c.tabs.entry.Text; got != "SELECT 1;\r\nSELECT 2;\r\n" {
		t.Fatalf("editor text = %q", got)
	}
	if !c.tabs.items[0].dirty || !strings.HasSuffix(c.tabs.items[0].item.Text, "●") {
		t.Fatalf("loaded tab not marked dirty: dirty=%v title=%q", c.tabs.items[0].dirty, c.tabs.items[0].item.Text)
	}

	// Saving normalises line endings so a script shared with a non-Windows
	// tool stays clean.
	var buf strings.Builder
	if err := c.saveScript(&buf); err != nil {
		t.Fatalf("saveScript: %v", err)
	}
	if want := "SELECT 1;\nSELECT 2;\n"; buf.String() != want {
		t.Fatalf("saved = %q, want %q", buf.String(), want)
	}
}

// TestScriptGuards checks the two refusals: binary content and oversize files.
func TestScriptGuards(t *testing.T) {

	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildQueryView())
	c.tabs.entry.setTextSilently("keep me")

	if err := c.loadScript(strings.NewReader("SELECT\x001")); err == nil {
		t.Fatal("binary payload accepted")
	}
	if err := c.loadScript(strings.NewReader(strings.Repeat("a", maxScriptBytes+1))); err == nil {
		t.Fatal("oversize payload accepted")
	}
	if c.tabs.entry.Text != "keep me" {
		t.Fatalf("refused loads changed the editor: %q", c.tabs.entry.Text)
	}
}
