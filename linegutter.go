package main

// Line-number gutter for the SQL editor. Fyne's Entry has no line numbers and
// keeps its scroll widget unexported, so the gutter:
//   - renders the numbers in a RichText with the SAME font/size as the editor,
//     so the per-line pitch matches automatically (no line-height guessing);
//   - mirrors the editor's vertical scroll offset, read from the Entry's
//     unexported "scroll" widget via reflection (Fyne v2.8.2, pinned in go.mod);
//   - hooks that scroll's OnScrolled (the Entry never sets it) to stay in sync.
// If reflection fails the gutter degrades to static numbers (no scroll sync)
// rather than crashing. gutterPad nudges the whole column if a constant offset
// is ever needed for visual alignment.

import (
	"reflect"
	"strconv"
	"strings"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// gutterWidth is the gutter column width (fits ~5 digits).
const lineNumWidth = float32(40)

// gutterPad is a constant vertical nudge for the numbers vs the editor text
// (0 = rely on matching font metrics; tweak only if the column looks offset).
const gutterPad = float32(0)

// entryScroll returns the Entry's internal *container.Scroll (unexported field
// "scroll") via reflection, or nil when unavailable. Fails soft.
func entryScroll(e *widget.Entry) (sc *container.Scroll) {
	if e == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			sc = nil
		}
	}()
	v := reflect.ValueOf(e).Elem().FieldByName("scroll")
	if !v.IsValid() || !v.CanAddr() {
		return nil
	}
	return *(**container.Scroll)(unsafe.Pointer(v.UnsafeAddr()))
}

// lineGutter shows 1..N line numbers scroll-synced to an editor Entry.
type lineGutter struct {
	widget.BaseWidget
	editor *widget.Entry
	rich   *widget.RichText
	scroll *container.Scroll
	lines  int
}

func newLineGutter(editor *widget.Entry) *lineGutter {
	g := &lineGutter{editor: editor}
	g.ExtendBaseWidget(g)
	// Mirror the editor's scrolling into the gutter.
	if sc := entryScroll(editor); sc != nil {
		prev := sc.OnScrolled
		sc.OnScrolled = func(pos fyne.Position) {
			if prev != nil {
				prev(pos)
			}
			g.syncOffset()
		}
	}
	return g
}

func (g *lineGutter) CreateRenderer() fyne.WidgetRenderer {
	g.rich = widget.NewRichText()
	g.rich.Wrapping = fyne.TextWrapOff
	g.scroll = container.NewVScroll(g.rich)
	g.sync()
	return widget.NewSimpleRenderer(g.scroll)
}

func (g *lineGutter) MinSize() fyne.Size { return fyne.NewSize(lineNumWidth, 1) }

func (g *lineGutter) editorScrollY() float32 {
	if sc := entryScroll(g.editor); sc != nil {
		return sc.Offset.Y
	}
	return 0
}

// sync rebuilds the numbers when the line count changes, then mirrors scroll.
func (g *lineGutter) sync() {
	if g.rich == nil {
		return
	}
	n := 1
	if g.editor != nil && g.editor.Text != "" {
		n = strings.Count(g.editor.Text, "\n") + 1
	}
	if n != g.lines {
		g.lines = n
		var b strings.Builder
		for i := 1; i <= n; i++ {
			if i > 1 {
				b.WriteByte('\n')
			}
			b.WriteString(strconv.Itoa(i))
		}
		g.rich.Segments = []widget.RichTextSegment{
			&widget.TextSegment{
				Text: b.String(),
				Style: widget.RichTextStyle{
					Alignment: fyne.TextAlignTrailing,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			},
		}
		g.rich.Refresh()
	}
	g.syncOffset()
}

func (g *lineGutter) syncOffset() {
	if g.scroll == nil {
		return
	}
	if y := g.editorScrollY() + gutterPad; g.scroll.Offset.Y != y {
		g.scroll.Offset.Y = y
	}
	g.scroll.Refresh()
}
