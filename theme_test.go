package main

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// rgba packs the bytes the tests assert against, so a colour expectation reads
// as the hex it came from.
func rgba(r, g, b, a uint8) color.Color {
	return color.NRGBA{R: r, G: g, B: b, A: a}
}

// TestTPColorLight pins the light palette's headline colours. These are the
// numbers the window actually paints, so a regression (a typo, a wrong hex)
// shows up here rather than as a screenshot someone has to eyeball.
func TestTPColorLight(t *testing.T) {
	cases := map[fyne.ThemeColorName]color.Color{
		theme.ColorNameBackground:      rgba(0xF2, 0xF3, 0xF5, 0xFF), // window grey
		theme.ColorNameMenuBackground:  rgba(0xF7, 0xF8, 0xF9, 0xFF), // panel grey
		theme.ColorNameInputBackground: rgba(0xFF, 0xFF, 0xFF, 0xFF), // content white
		theme.ColorNameSeparator:       rgba(0xD8, 0xDB, 0xDE, 0xFF), // 1px line
		theme.ColorNameInputBorder:     rgba(0xD8, 0xDB, 0xDE, 0xFF),
		theme.ColorNamePrimary:         rgba(0x1E, 0x6F, 0xD9, 0xFF), // accent blue
		theme.ColorNameForeground:      rgba(0x33, 0x33, 0x3A, 0xFF),
	}
	for name, want := range cases {
		got, ok := tpColor(name, theme.VariantLight)
		if !ok {
			t.Fatalf("tpColor(%q, light) not defined", name)
		}
		assertColorEqual(t, string(name), got, want)
	}
}

// TestTPColorDark pins the dark palette's headline colours - the same names, so
// a colour can never be "the new one" in one variant and "the old one" in the
// other.
func TestTPColorDark(t *testing.T) {
	cases := map[fyne.ThemeColorName]color.Color{
		theme.ColorNameBackground:      rgba(0x19, 0x1A, 0x1C, 0xFF),
		theme.ColorNameMenuBackground:  rgba(0x21, 0x22, 0x25, 0xFF),
		theme.ColorNameInputBackground: rgba(0x21, 0x22, 0x25, 0xFF),
		theme.ColorNameSeparator:       rgba(0x37, 0x38, 0x3B, 0xFF),
		theme.ColorNamePrimary:         rgba(0x4C, 0x8F, 0xF7, 0xFF), // lifted so it still reads
		theme.ColorNameForeground:      rgba(0xE4, 0xE5, 0xE7, 0xFF),
	}
	for name, want := range cases {
		got, ok := tpColor(name, theme.VariantDark)
		if !ok {
			t.Fatalf("tpColor(%q, dark) not defined", name)
		}
		assertColorEqual(t, string(name), got, want)
	}
}

// TestTPColorSameNames keeps the two palettes in lockstep: every name defined
// for light must also be defined for dark. A name missing from one side is what
// lets a colour silently fall through to the built-in theme, which is exactly
// the half-themed window this table was built to prevent.
func TestTPColorSameNames(t *testing.T) {
	for name := range tpLight {
		if _, ok := tpDark[name]; !ok {
			t.Errorf("colour %q defined for light but not dark", name)
		}
	}
	for name := range tpDark {
		if _, ok := tpLight[name]; !ok {
			t.Errorf("colour %q defined for dark but not light", name)
		}
	}
}

// TestTPColorFallback is the negative case that keeps the fallback honest: a
// name this theme never chose must report "not found" so the caller falls back
// to the built-in theme instead of inventing a value.
func TestTPColorFallback(t *testing.T) {
	if _, ok := tpColor(theme.ColorNameSuccess, theme.VariantLight); ok {
		t.Error("tpColor returned a value for an undefined colour name")
	}
}

// TestTPColorFollowVariant pins that the "follow system" sentinel resolves to
// the light palette (tpColor treats anything that is not explicitly dark as
// light), so the palette function and the theme agree on what "not dark" means.
func TestTPColorFollowVariant(t *testing.T) {
	light, _ := tpColor(theme.ColorNameBackground, theme.VariantLight)
	follow, ok := tpColor(theme.ColorNameBackground, tpFollow)
	if !ok {
		t.Fatal("tpColor(background, follow) not defined")
	}
	if !sameColor(light, follow) {
		t.Error("follow variant did not resolve to the light palette")
	}
}

// TestTPSize pins the only size overrides and that everything else is left to
// the built-in theme.
func TestTPSize(t *testing.T) {
	cases := map[fyne.ThemeSizeName]float32{
		theme.SizeNameInnerPadding: 6,
		theme.SizeNameScrollBar:    10,
		theme.SizeNameInputRadius:  4,
		theme.SizeNameButtonRadius: 4,
	}
	for name, want := range cases {
		got, ok := tpSize(name)
		if !ok {
			t.Fatalf("tpSize(%q) not defined", name)
		}
		if got != want {
			t.Errorf("tpSize(%q) = %v, want %v", name, got, want)
		}
	}
	if _, ok := tpSize(theme.SizeNameText); ok {
		t.Error("tpSize returned a value for an unoverridden size name")
	}
}

// TestTPThemeColorFallback checks the theme wrapper, not just the table: a
// covered colour comes from the palette and an uncovered one falls through to
// the built-in theme rather than panicking or returning nil.
func TestTPThemeColorFallback(t *testing.T) {
	th := newTablePlusTheme(tpFollow)
	if got := th.Color(theme.ColorNameBackground, theme.VariantLight); got == nil {
		t.Fatal("covered colour returned nil")
	}
	if got := th.Color(theme.ColorNameSuccess, theme.VariantLight); got == nil {
		t.Fatal("uncovered colour fell through to nil instead of the base theme")
	}
}

// TestTPThemePinnedVariant checks that a pinned (non-follow) theme forces its
// variant regardless of what the framework passes in - that is the whole point
// of keeping the variant on the theme rather than trusting the caller.
func TestTPThemePinnedVariant(t *testing.T) {
	th := newTablePlusTheme(theme.VariantDark)
	// The framework asks for light, but the theme is pinned dark: the answer
	// must still be the dark background.
	got, _ := tpColor(theme.ColorNameBackground, theme.VariantLight)
	pinned := th.Color(theme.ColorNameBackground, theme.VariantLight)
	if sameColor(got, pinned) {
		t.Error("pinned-dark theme answered the caller's light request")
	}
}

func assertColorEqual(t *testing.T, label string, got, want color.Color) {
	t.Helper()
	if !sameColor(got, want) {
		gr, gg, gb, ga := got.RGBA()
		wr, wg, wb, wa := want.RGBA()
		t.Errorf("%s = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
			label, gr>>8, gg>>8, gb>>8, ga>>8, wr>>8, wg>>8, wb>>8, wa>>8)
	}
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
