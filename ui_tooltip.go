package main

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Fyne ships no tooltip widget, and the icon-only buttons in the client
// (清空, 侧栏新建/刷新/搜索, 历史重跑/清空) are undiscoverable without one.
// tooltipHost wraps such a widget: hovering it for a moment grows a small
// bubble under it on the canvas overlay layer, so nothing below shifts.

const tooltipDelay = 600 * time.Millisecond

type tooltipHost struct {
	widget.BaseWidget
	impl    *fyne.Container
	tooltip string
	timer   *time.Timer
	tip     fyne.CanvasObject // the bubble, while shown
}

// newTooltipHost wraps target so a hovering mouse is met with a hint after
// a short delay. The hint never fires on a disabled widget - a greyed 重跑
// button would only apologise for itself.
func newTooltipHost(target fyne.CanvasObject, tooltip string) *tooltipHost {
	h := &tooltipHost{tooltip: tooltip, impl: container.NewVBox(target)}
	h.ExtendBaseWidget(h)
	return h
}

// CreateRenderer implements fyne.Widget: the host is a pass-through, its one
// child is everything that gets drawn.
func (h *tooltipHost) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(h.impl)
}

// MinSize keeps the host and the wrapped target measuring identically, so
// attaching a tooltip never nudges the layout.
func (h *tooltipHost) MinSize() fyne.Size {
	return h.impl.MinSize()
}

// MouseIn starts the hint clock on entry, unless the wrapped widget is
// disabled, in which case there is nothing to explain.
func (h *tooltipHost) MouseIn(_ *desktop.MouseEvent) {
	if btn, ok := h.impl.Objects[0].(*widget.Button); ok && btn.Disabled() {
		return
	}
	if !h.tipShown() {
		h.timer = time.AfterFunc(tooltipDelay, func() {
			// Timers fire off the UI thread; the canvas is the UI thread's.
			fyne.Do(func() { h.showTip() })
		})
	}
}

// MouseMoved swallows movement: the hint follows nothing, it waits out its
// delay after the pointer comes to rest inside the widget.
func (h *tooltipHost) MouseMoved(_ *desktop.MouseEvent) {}

// MouseOut abandons the pending hint and withdraws a shown one.
func (h *tooltipHost) MouseOut() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	if h.tipShown() {
		fyne.Do(func() { h.hideTip() })
	}
}

func (h *tooltipHost) tipShown() bool {
	return h.tip != nil
}

func (h *tooltipHost) showTip() {
	if h.tip != nil {
		return
	}
	driver := fyne.CurrentApp().Driver()
	c := driver.CanvasForObject(h)
	if c == nil {
		return
	}
	text := widget.NewLabel(h.tooltip)
	// The bubble is one rounded, stroked card: a menu-background fill keeps
	// it legible on both themes, and the padding gives the text breathing
	// room instead of hugging the stroke.
	bubble := container.NewStack(
		&canvas.Rectangle{
			FillColor:    theme.Color(theme.ColorNameMenuBackground),
			StrokeColor:  theme.Color(theme.ColorNameShadow),
			StrokeWidth:  1,
			CornerRadius: 6,
		},
		container.NewPadded(text),
	)
	pos := driver.AbsolutePositionForObject(h)
	pos.Y += h.MinSize().Height + 4
	bubble.Resize(bubble.MinSize())
	bubble.Move(pos)
	c.Overlays().Add(bubble)
	h.tip = bubble
}

func (h *tooltipHost) hideTip() {
	if h.tip == nil {
		return
	}
	if c := fyne.CurrentApp().Driver().CanvasForObject(h); c != nil {
		c.Overlays().Remove(h.tip)
	}
	h.tip = nil
}

// attachTooltip is the one-line call sites use: wrap the icon-only button and
// pass the hint text it should offer. The host measures exactly like the
// button, so it can replace the button in any layout.
func attachTooltip(w fyne.CanvasObject, text string) fyne.CanvasObject {
	return newTooltipHost(w, text)
}
