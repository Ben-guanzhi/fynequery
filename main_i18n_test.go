package main

// The Fyne widget.Select default placeholder "(Select one)" has no zh-Hans
// translation, so on a Chinese system NewSelect logs a "Translation failure"
// from its constructor. main() registers the missing key via
// lang.AddTranslationsForLocale before any UI is built. This test pins that
// registration: the payload must parse, and constructing a Select (which runs
// the placeholder lookup) must succeed afterwards.

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/lang"
	"fyne.io/fyne/v2/widget"
)

func TestSelectPlaceholderTranslationRegistered(t *testing.T) {
	const payload = `{"(Select one)":"（请选择）"}`
	if err := lang.AddTranslationsForLocale([]byte(payload), fyne.Locale("zh-Hans")); err != nil {
		t.Fatalf("register (Select one) translation: %v", err)
	}
	// NewSelect looks the placeholder up in its constructor; this must not
	// error out or panic once the key is registered.
	sel := widget.NewSelect([]string{"a", "b"}, nil)
	if sel == nil {
		t.Fatal("NewSelect returned nil")
	}
}
