package main

// consolePane is the workspace's bottom log console (the mockup's
// "控制台日志" panel): a scrollable, timestamped log with an empty-state hint
// and a trash button that clears it. Writes must happen on the UI thread
// (event handlers or safeDo).

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// consoleMaxLines caps the in-memory log so long sessions do not grow forever.
const consoleMaxLines = 500

type consolePane struct {
	widget.BaseWidget
	lines  []string
	label  *widget.Label
	scroll *container.Scroll
	hint   fyne.CanvasObject
	stack  *fyne.Container
	count  *widget.Label
}

func newConsolePane() *consolePane {
	c := &consolePane{}
	c.ExtendBaseWidget(c)
	return c
}

// log appends one entry, prefixes it with the local time and keeps the newest
// consoleMaxLines entries.
func (c *consolePane) log(text string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return
	}
	ts := time.Now().Format("15:04:05")
	for _, ln := range strings.Split(text, "\n") {
		c.lines = append(c.lines, ts+"  "+ln)
	}
	if len(c.lines) > consoleMaxLines {
		c.lines = append([]string(nil), c.lines[len(c.lines)-consoleMaxLines:]...)
	}
	c.refresh()
}

// clear empties the console and restores the hint.
func (c *consolePane) clear() {
	c.lines = nil
	c.refresh()
}

// copyAll copies the whole log to the clipboard (no-op when empty).
func (c *consolePane) copyAll() {
	if len(c.lines) == 0 {
		return
	}
	fyne.CurrentApp().Clipboard().SetContent(strings.Join(c.lines, "\n"))
}

func (c *consolePane) refresh() {
	if c.label == nil {
		return
	}
	c.label.SetText(strings.Join(c.lines, "\n"))
	if c.count != nil {
		if len(c.lines) == 0 {
			c.count.SetText("")
		} else {
			c.count.SetText(fmt.Sprintf("%d 行", len(c.lines)))
		}
	}
	if c.stack != nil {
		if len(c.lines) == 0 {
			c.hint.Show()
			c.scroll.Hide()
		} else {
			c.hint.Hide()
			c.scroll.Show()
			c.scroll.ScrollToBottom()
		}
		c.stack.Refresh()
	}
}

func (c *consolePane) CreateRenderer() fyne.WidgetRenderer {
	c.label = widget.NewLabel("")
	c.label.Wrapping = fyne.TextWrapWord
	c.label.TextStyle = fyne.TextStyle{Monospace: true}

	c.scroll = container.NewVScroll(c.label)
	c.scroll.Hide()

	hint := widget.NewLabelWithStyle("控制台日志，请执行查询或选择表格\n以查看日志",
		fyne.TextAlignCenter, fyne.TextStyle{})
	hint.Importance = widget.LowImportance
	c.hint = container.NewCenter(hint)

	c.stack = container.NewStack(c.hint, c.scroll)

	clearBtn := newToolButton(theme.DeleteIcon(), func() { c.clear() })
	clearBtn.small = true
	copyBtn := newToolButton(theme.ContentCopyIcon(), func() { c.copyAll() })
	copyBtn.small = true

	c.count = widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Italic: true})
	c.count.Importance = widget.LowImportance

	bottom := container.NewBorder(nil, nil,
		container.NewHBox(container.NewPadded(copyBtn), container.NewPadded(clearBtn)),
		container.NewPadded(c.count),
		layout.NewSpacer(),
	)

	root := container.NewBorder(nil, bottom, nil, nil, c.stack)
	return widget.NewSimpleRenderer(root)
}
