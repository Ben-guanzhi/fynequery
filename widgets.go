package main

// Custom TablePlus-style widgets: tab bar, segmented control, badges,
// chips, toolbar buttons, breadcrumb and status bar.

import (
	"fmt"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// ---------------------------------------------------------------------------
// minWidth: forces a minimum width on a child (Border layouts size side
// objects from MinSize).

type minWidth struct {
	widget.BaseWidget
	child fyne.CanvasObject
	w     float32
}

func newMinWidth(child fyne.CanvasObject, w float32) *minWidth {
	m := &minWidth{child: child, w: w}
	m.ExtendBaseWidget(m)
	return m
}

func (m *minWidth) MinSize() fyne.Size {
	s := m.child.MinSize()
	if s.Width < m.w {
		s.Width = m.w
	}
	return s
}

func (m *minWidth) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(m.child)
}

// ---------------------------------------------------------------------------
// panelBackground: paints a theme-driven background color behind a child.

type panelBackground struct {
	widget.BaseWidget
	child   fyne.CanvasObject
	colorFn func() color.Color
}

func newPanelBackground(child fyne.CanvasObject, colorFn func() color.Color) *panelBackground {
	pb := &panelBackground{child: child, colorFn: colorFn}
	pb.ExtendBaseWidget(pb)
	return pb
}

func (pb *panelBackground) CreateRenderer() fyne.WidgetRenderer {
	rect := canvas.NewRectangle(pb.colorFn())
	return &panelBgRenderer{pb: pb, rect: rect}
}

type panelBgRenderer struct {
	pb   *panelBackground
	rect *canvas.Rectangle
}

func (r *panelBgRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.rect, r.pb.child}
}

func (r *panelBgRenderer) Layout(size fyne.Size) {
	r.rect.Resize(size)
	r.pb.child.Resize(size)
}

func (r *panelBgRenderer) MinSize() fyne.Size { return r.pb.child.MinSize() }

func (r *panelBgRenderer) Destroy() {}

func (r *panelBgRenderer) Refresh() {
	r.rect.FillColor = r.pb.colorFn()
	canvas.Refresh(r.pb)
}

// ---------------------------------------------------------------------------
// accentSplit: a two-pane split with a draggable divider painted in the
// TablePlus accent colour. Used for the resizable sidebar and detail panel so
// the divider doubles as their coloured border.

const accentSplitThickness = float32(3)

type accentSplit struct {
	widget.BaseWidget
	Offset   float64
	Leading  fyne.CanvasObject
	Trailing fyne.CanvasObject

	offsetUpdated bool // next refresh is an offset-only update (move/resize)
}

func newAccentSplit(leading, trailing fyne.CanvasObject) *accentSplit {
	s := &accentSplit{Offset: 0.5, Leading: leading, Trailing: trailing}
	s.ExtendBaseWidget(s)
	return s
}

// SetOffset sets the divider position as a ratio (0.0-1.0) of the free width.
func (s *accentSplit) SetOffset(offset float64) {
	if s.Offset == offset {
		return
	}
	s.Offset = offset
	s.offsetUpdated = true
	s.Refresh()
}

func (s *accentSplit) CreateRenderer() fyne.WidgetRenderer {
	d := newAccentDivider(s)
	return &accentSplitRenderer{
		split:   s,
		divider: d,
		objects: []fyne.CanvasObject{s.Leading, d, s.Trailing},
	}
}

type accentSplitRenderer struct {
	split   *accentSplit
	divider *accentDivider
	objects []fyne.CanvasObject
}

func (r *accentSplitRenderer) Destroy() {}

func (r *accentSplitRenderer) Layout(size fyne.Size) {
	var leadingPos, trailingPos, dividerPos fyne.Position
	var leadingSize, trailingSize, dividerSize fyne.Size

	dividerVisible := r.split.Leading.Visible() && r.split.Trailing.Visible()
	switch {
	case !r.split.Leading.Visible():
		trailingSize = size
	case !r.split.Trailing.Visible():
		leadingSize = size
	case dividerVisible:
		lw, tw := r.splitLengths(size.Width)
		leadingSize = fyne.NewSize(lw, size.Height)
		dividerPos = fyne.NewPos(lw, 0)
		dividerSize = fyne.NewSize(accentSplitThickness, size.Height)
		trailingPos = fyne.NewPos(lw+accentSplitThickness, 0)
		trailingSize = fyne.NewSize(tw, size.Height)
	}

	r.divider.Move(dividerPos)
	r.divider.Resize(dividerSize)
	r.divider.Hidden = !dividerVisible

	r.split.Leading.Move(leadingPos)
	r.split.Leading.Resize(leadingSize)
	r.split.Trailing.Move(trailingPos)
	r.split.Trailing.Resize(trailingSize)
	canvas.Refresh(r.divider)
}

func (r *accentSplitRenderer) splitLengths(total float32) (float32, float32) {
	available := float64(total - accentSplitThickness)
	if available <= 0 {
		return 0, 0
	}
	lMin := float64(r.split.Leading.MinSize().Width)
	tMin := float64(r.split.Trailing.MinSize().Width)
	min := lMin / available
	max := 1 - tMin/available
	offset := r.split.Offset
	if min <= max {
		if offset < min {
			offset = min
		}
		if offset > max {
			offset = max
		}
	} else {
		offset = lMin / (lMin + tMin)
	}
	lw := offset * available
	return float32(lw), float32(available - lw)
}

func (r *accentSplitRenderer) MinSize() fyne.Size {
	s := fyne.NewSize(0, 0)
	dividerVisible := r.split.Leading.Visible() && r.split.Trailing.Visible()
	for i, o := range r.objects {
		if (i == 1 && !dividerVisible) || (i != 1 && !o.Visible()) {
			continue
		}
		min := o.MinSize()
		s.Width += min.Width
		s.Height = fyne.Max(s.Height, min.Height)
	}
	return s
}

func (r *accentSplitRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *accentSplitRenderer) Refresh() {
	if r.split.offsetUpdated {
		r.Layout(r.split.Size())
		r.split.offsetUpdated = false
		return
	}
	r.objects[0] = r.split.Leading
	r.objects[2] = r.split.Trailing
	r.Layout(r.split.Size())
	r.split.Leading.Refresh()
	r.divider.Refresh()
	r.split.Trailing.Refresh()
	canvas.Refresh(r.split)
}

// accentDivider is the draggable handle between the two accentSplit panes.
type accentDivider struct {
	widget.BaseWidget
	split *accentSplit
	line  *canvas.Rectangle

	startDragOff *fyne.Position
	dragPos      fyne.Position
}

var (
	_ fyne.Draggable     = (*accentDivider)(nil)
	_ desktop.Cursorable = (*accentDivider)(nil)
	_ desktop.Hoverable  = (*accentDivider)(nil)
)

func newAccentDivider(s *accentSplit) *accentDivider {
	d := &accentDivider{split: s, line: canvas.NewRectangle(pal().dividerLine)}
	d.ExtendBaseWidget(d)
	return d
}

func (d *accentDivider) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(d.line)
}

func (d *accentDivider) Cursor() desktop.Cursor { return desktop.HResizeCursor }

func (d *accentDivider) DragEnd() { d.startDragOff = nil }

func (d *accentDivider) Dragged(e *fyne.DragEvent) {
	if d.startDragOff == nil {
		d.dragPos = d.Position().Add(e.Position)
		start := e.Position.Subtract(e.Dragged)
		d.startDragOff = &start
	} else {
		d.dragPos = d.dragPos.Add(e.Dragged)
	}

	free := float64(d.split.Size().Width - accentSplitThickness)
	if free <= 0 {
		return
	}
	x, _ := d.dragPos.Components()
	offset := float64(x-d.startDragOff.X) / free
	if min := float64(d.split.Leading.MinSize().Width) / free; offset < min {
		offset = min
	}
	if max := 1 - float64(d.split.Trailing.MinSize().Width)/free; offset > max {
		offset = max
	}
	d.split.SetOffset(offset)
}

func (d *accentDivider) MouseIn(*desktop.MouseEvent) {
	d.line.FillColor = pal().accent
	d.line.Refresh()
}
func (d *accentDivider) MouseMoved(*desktop.MouseEvent) {}
func (d *accentDivider) MouseOut() {
	d.line.FillColor = pal().dividerLine
	d.line.Refresh()
}

// ---------------------------------------------------------------------------
// type badge: rounded square with two letters, colored by DB type.

func typeBadge(t DBType, size float32) fyne.CanvasObject {
	bg := hex(t.Color)
	fg := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	if t.Light {
		fg = color.RGBA{R: 0x2B, G: 0x2B, B: 0x2B, A: 255}
	}
	rect := canvas.NewRectangle(bg)
	rect.CornerRadius = size * 0.28
	rect.SetMinSize(fyne.NewSize(size, size))
	label := canvas.NewText(t.Code, fg)
	label.TextSize = size * 0.42
	label.TextStyle.Bold = true
	label.Alignment = fyne.TextAlignCenter
	return container.NewStack(rect, container.NewPadded(label))
}

// dbPill is the toolbar breadcrumb badge (slightly pill-shaped).
func dbPill(t DBType) fyne.CanvasObject {
	rect := canvas.NewRectangle(hex(t.Color))
	rect.CornerRadius = 7
	fg := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	if t.Light {
		fg = color.RGBA{R: 0x2B, G: 0x2B, B: 0x2B, A: 255}
	}
	label := canvas.NewText(t.Code, fg)
	label.TextSize = 12
	label.TextStyle.Bold = true
	return container.NewStack(rect, label)
}

// tagChip renders a small outlined chip like "loc" / "tes".
func tagChip(tg Tag) fyne.CanvasObject {
	c := hex(tg.Color)
	border := canvas.NewRectangle(color.RGBA{R: 0, G: 0, B: 0, A: 0})
	border.StrokeColor = c
	border.StrokeWidth = 1
	border.CornerRadius = 7
	label := canvas.NewText(tg.Label, c)
	label.TextSize = 10
	return container.NewStack(border, container.NewPadded(label))
}

// ---------------------------------------------------------------------------
// clickableIcon: hover-highlighted icon button used in toolbars.

type clickableIcon struct {
	widget.BaseWidget
	icon     fyne.Resource
	onTap    func()
	small    bool
	hover    bool
	selected bool
}

func newToolButton(icon fyne.Resource, onTap func()) *clickableIcon {
	b := &clickableIcon{icon: icon, onTap: onTap}
	b.ExtendBaseWidget(b)
	return b
}

// setSelected highlights (or clears) the toggle button's pressed state.
func (c *clickableIcon) setSelected(v bool) {
	if c.selected != v {
		c.selected = v
		c.Refresh()
	}
}

func (c *clickableIcon) Tapped(_ *fyne.PointEvent) {
	if c.onTap != nil {
		c.onTap()
	}
}

func (c *clickableIcon) Cursor() desktop.Cursor { return desktop.DefaultCursor }

func (c *clickableIcon) MouseIn(*desktop.MouseEvent) {
	c.hover = true
	c.Refresh()
}

func (c *clickableIcon) MouseOut() {
	c.hover = false
	c.Refresh()
}

func (c *clickableIcon) MouseMoved(*desktop.MouseEvent) {}

func (c *clickableIcon) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.RGBA{R: 0, G: 0, B: 0, A: 0})
	bg.CornerRadius = 6
	icon := widget.NewIcon(c.icon)
	return &clickableIconRenderer{b: c, bg: bg, icon: icon}
}

type clickableIconRenderer struct {
	b    *clickableIcon
	bg   *canvas.Rectangle
	icon *widget.Icon
}

func (r *clickableIconRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{container.NewStack(r.bg, r.icon)}
}

func (r *clickableIconRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.icon.Resize(fyne.NewSize(size.Width-8, size.Height-8))
	r.icon.Move(fyne.NewPos(4, 4))
}

func (r *clickableIconRenderer) MinSize() fyne.Size {
	s := float32(28)
	if r.b.small {
		s = 22
	}
	return fyne.NewSize(s, s)
}

func (r *clickableIconRenderer) Destroy() {}

func (r *clickableIconRenderer) Refresh() {
	p := pal()
	switch {
	case r.b.selected:
		r.bg.FillColor = p.segmentActive
	case r.b.hover:
		r.bg.FillColor = p.hover
	default:
		r.bg.FillColor = color.RGBA{R: 0, G: 0, B: 0, A: 0}
	}
	r.bg.Refresh()
}

// ---------------------------------------------------------------------------
// tabLabel: one tab in the top tab bar.

type tabLabel struct {
	widget.BaseWidget
	title     string
	active    bool
	closeable bool
	hover     bool
	closing   bool
	onTap     func()
	onClose   func()
}

func newTabLabel(title string, closeable bool, onTap, onClose func()) *tabLabel {
	t := &tabLabel{title: title, closeable: closeable, onTap: onTap, onClose: onClose}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tabLabel) setActive(a bool) {
	if t.active != a {
		t.active = a
		t.Refresh()
	}
}

func (t *tabLabel) setTitle(s string) {
	t.title = s
	t.Refresh()
}

func (t *tabLabel) Tapped(_ *fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}

func (t *tabLabel) Cursor() desktop.Cursor { return desktop.DefaultCursor }

func (t *tabLabel) MouseIn(*desktop.MouseEvent) {
	t.hover = true
	t.Refresh()
}

func (t *tabLabel) MouseOut() {
	t.hover = false
	t.Refresh()
}

func (t *tabLabel) MouseMoved(*desktop.MouseEvent) {}

func (t *tabLabel) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.RGBA{R: 0, G: 0, B: 0, A: 0})
	bg.CornerRadius = 6
	underline := canvas.NewRectangle(pal().tabUnderline)
	label := canvas.NewText(t.title, pal().activeTabFG)
	label.TextSize = 13
	var closer *clickableIcon
	if t.closeable {
		closer = newToolButton(theme.CancelIcon(), func() {
			if t.onClose != nil {
				t.onClose()
			}
		})
		closer.small = true
	}
	return &tabLabelRenderer{t: t, bg: bg, underline: underline, label: label, closer: closer}
}

type tabLabelRenderer struct {
	t         *tabLabel
	bg        *canvas.Rectangle
	underline *canvas.Rectangle
	label     *canvas.Text
	closer    *clickableIcon
}

func (r *tabLabelRenderer) Objects() []fyne.CanvasObject {
	objs := []fyne.CanvasObject{r.bg, r.label, r.underline}
	if r.closer != nil {
		objs = append(objs, r.closer)
	}
	return objs
}

func (r *tabLabelRenderer) MinSize() fyne.Size {
	w := r.label.MinSize().Width + 30
	if r.t.closeable {
		w += 26
	}
	return fyne.NewSize(w, 36)
}

func (r *tabLabelRenderer) Destroy() {}

func (r *tabLabelRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.underline.Resize(fyne.NewSize(size.Width, 2.5))
	r.underline.Move(fyne.NewPos(0, size.Height-2.5))
	lh := r.label.MinSize()
	r.label.Move(fyne.NewPos(13, (size.Height-lh.Height)/2))
	if r.closer != nil {
		r.closer.Move(fyne.NewPos(size.Width-25, (size.Height-22)/2))
		r.closer.Resize(fyne.NewSize(22, 22))
	}
}

func (r *tabLabelRenderer) Refresh() {
	p := pal()
	if r.t.active {
		r.bg.FillColor = p.activeTabBG
		r.label.Color = p.activeTabFG
		r.underline.FillColor = p.tabUnderline
		r.label.TextStyle.Bold = true
	} else {
		if r.t.hover {
			r.bg.FillColor = p.hover
		} else {
			r.bg.FillColor = p.tabBG
		}
		r.label.Color = p.inactiveTabFG
		r.underline.FillColor = colorTransparent // hidden
		r.label.TextStyle.Bold = false
	}
	r.label.Text = r.t.title
	r.bg.Refresh()
	r.label.Refresh()
	r.underline.Refresh()
}

// ---------------------------------------------------------------------------
// tabBar: a horizontal strip of tabLabels with an underline and "+" button.

type tabBar struct {
	widget.BaseWidget
	tabs       []*tabLabel
	current    int
	onChange   func(int)
	onAdd      func()
	onCloseTab func(int)
}

func newTabBar(onChange func(int), onAdd func()) *tabBar {
	b := &tabBar{onChange: onChange, onAdd: onAdd}
	b.ExtendBaseWidget(b)
	return b
}

func (b *tabBar) addTab(title string, closeable bool) *tabLabel {
	var t *tabLabel
	t = newTabLabel(title, closeable, func() {
		idx := b.indexOf(t)
		if idx >= 0 && idx != b.current && b.onChange != nil {
			b.onChange(idx)
		}
	}, func() {
		if b.onCloseTab != nil {
			b.onCloseTab(b.indexOf(t))
		}
	})
	b.tabs = append(b.tabs, t)
	b.Refresh()
	return t
}

func (b *tabBar) indexOf(t *tabLabel) int {
	for i, x := range b.tabs {
		if x == t {
			return i
		}
	}
	return -1
}

func (b *tabBar) removeTab(i int) {
	if i < 0 || i >= len(b.tabs) {
		return
	}
	b.tabs = append(b.tabs[:i], b.tabs[i+1:]...)
	if b.current >= len(b.tabs) {
		b.current = len(b.tabs) - 1
	}
	b.Refresh()
}

func (b *tabBar) setActive(i int) {
	b.current = i
	for idx, t := range b.tabs {
		t.setActive(idx == i)
	}
	b.Refresh()
}

// onCloseTab is assigned by the owner to handle tab closing.
func (b *tabBar) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(pal().stripBG)
	addBtn := newToolButton(theme.ContentAddIcon(), func() {
		if b.onAdd != nil {
			b.onAdd()
		}
	})
	return &tabBarRenderer{b: b, bg: bg, addBtn: addBtn}
}

type tabBarRenderer struct {
	b      *tabBar
	bg     *canvas.Rectangle
	addBtn *clickableIcon
	size   fyne.Size
}

func (r *tabBarRenderer) Objects() []fyne.CanvasObject {
	objs := []fyne.CanvasObject{r.bg}
	for _, t := range r.b.tabs {
		objs = append(objs, t)
	}
	return append(objs, r.addBtn)
}

func (r *tabBarRenderer) MinSize() fyne.Size {
	w := float32(44)
	for _, t := range r.b.tabs {
		w += t.MinSize().Width + 2
	}
	return fyne.NewSize(w, 40)
}

func (r *tabBarRenderer) Destroy() {}

func (r *tabBarRenderer) Layout(size fyne.Size) {
	r.size = size
	r.bg.Resize(size)
	// lay the tabs out left to right; "+" sits right after the last one,
	// like TablePlus's document strip.
	x := float32(6)
	for _, t := range r.b.tabs {
		tw := t.MinSize().Width
		t.Resize(fyne.NewSize(tw, size.Height))
		t.Move(fyne.NewPos(x, 0))
		x += tw + 2
	}
	r.addBtn.Move(fyne.NewPos(x+4, (size.Height-28)/2))
	r.addBtn.Resize(fyne.NewSize(28, 28))
}

func (r *tabBarRenderer) Refresh() {
	r.bg.FillColor = pal().stripBG
	r.bg.Refresh()
	for _, t := range r.b.tabs {
		t.Refresh()
	}
	// re-run the layout so added/removed tabs reposition
	if r.size.Width > 0 || r.size.Height > 0 {
		r.Layout(r.size)
	}
}

// ---------------------------------------------------------------------------
// segmented: TablePlus-style "Data | Structure" segmented control.

type segmented struct {
	widget.BaseWidget
	options  []string
	current  int
	onChange func(int)
	size     fyne.Size
}

func newSegmented(options []string, onChange func(int)) *segmented {
	s := &segmented{options: options, onChange: onChange}
	s.ExtendBaseWidget(s)
	return s
}

func (s *segmented) Tapped(e *fyne.PointEvent) {
	n := float32(len(s.options))
	if n == 0 || s.size.Width <= 0 {
		return
	}
	idx := int(e.Position.X*n/s.size.Width + 0.0001)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s.options) {
		idx = len(s.options) - 1
	}
	s.set(idx)
}

func (s *segmented) set(idx int) {
	if idx == s.current {
		return
	}
	s.current = idx
	s.Refresh()
	if s.onChange != nil {
		s.onChange(idx)
	}
}

func (s *segmented) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(pal().segmentBG)
	bg.CornerRadius = 7
	var pills []*canvas.Rectangle
	var labels []*canvas.Text
	for _, o := range s.options {
		pill := canvas.NewRectangle(pal().segmentActive)
		pill.CornerRadius = 6
		label := canvas.NewText(o, pal().segmentActiveFG)
		label.TextSize = 12
		labels = append(labels, label)
		pills = append(pills, pill)
	}
	return &segmentedRenderer{s: s, bg: bg, pills: pills, labels: labels}
}

type segmentedRenderer struct {
	s      *segmented
	bg     *canvas.Rectangle
	pills  []*canvas.Rectangle
	labels []*canvas.Text
}

func (r *segmentedRenderer) Objects() []fyne.CanvasObject {
	objs := []fyne.CanvasObject{r.bg}
	for i := range r.pills {
		objs = append(objs, r.pills[i], r.labels[i])
	}
	return objs
}

func (r *segmentedRenderer) MinSize() fyne.Size {
	return fyne.NewSize(float32(len(r.s.options))*86+8, 28)
}

func (r *segmentedRenderer) Destroy() {}

func (r *segmentedRenderer) Layout(size fyne.Size) {
	r.s.size = size
	r.bg.Resize(size)
	n := float32(len(r.s.options))
	w := (size.Width - 8) / n
	for i := range r.pills {
		x := 4 + float32(i)*w
		r.pills[i].Resize(fyne.NewSize(w-4, size.Height-6))
		r.pills[i].Move(fyne.NewPos(x+2, 3))
		lb := r.labels[i].MinSize()
		r.labels[i].Move(fyne.NewPos(x+(w-lb.Width)/2, (size.Height-lb.Height)/2))
	}
}

func (r *segmentedRenderer) Refresh() {
	for i := range r.pills {
		if i == r.s.current {
			r.pills[i].FillColor = pal().segmentActive
			r.pills[i].Show()
			r.labels[i].Color = pal().segmentActiveFG
		} else {
			r.pills[i].FillColor = color.RGBA{R: 0, G: 0, B: 0, A: 0}
			r.labels[i].Color = pal().segmentFG
		}
		r.labels[i].Refresh()
	}
}

// ---------------------------------------------------------------------------
// crumbBox: the rounded table-name field in the crumb bar.

type crumbBox struct {
	widget.BaseWidget
	text *canvas.Text
	bg   *canvas.Rectangle
	line *canvas.Rectangle
}

func newCrumbBox(text string) *crumbBox {
	c := &crumbBox{}
	c.text = canvas.NewText(text, pal().foreground)
	c.text.TextSize = 13
	c.text.TextStyle.Bold = true
	c.ExtendBaseWidget(c)
	return c
}

func (c *crumbBox) setText(s string) {
	c.text.Text = s
	c.Refresh()
}

func (c *crumbBox) CreateRenderer() fyne.WidgetRenderer {
	c.bg = canvas.NewRectangle(pal().crumbBG)
	c.bg.CornerRadius = 8
	c.line = canvas.NewRectangle(pal().crumbBorder)
	c.line.CornerRadius = 8
	c.line.StrokeWidth = 1
	return &crumbBoxRenderer{c: c}
}

type crumbBoxRenderer struct{ c *crumbBox }

func (r *crumbBoxRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.c.bg, r.c.line, r.c.text}
}

func (r *crumbBoxRenderer) MinSize() fyne.Size { return fyne.NewSize(120, 28) }

func (r *crumbBoxRenderer) Destroy() {}

func (r *crumbBoxRenderer) Layout(size fyne.Size) {
	r.c.bg.Resize(size)
	r.c.line.Resize(size)
	t := r.c.text.MinSize()
	r.c.text.Move(fyne.NewPos(14, (size.Height-t.Height)/2))
}

func (r *crumbBoxRenderer) Refresh() {
	p := pal()
	r.c.bg.FillColor = p.crumbBG
	r.c.line.StrokeColor = p.crumbBorder
	r.c.text.Color = p.foreground
	r.c.bg.Refresh()
	r.c.line.Refresh()
	r.c.text.Refresh()
}

// ---------------------------------------------------------------------------
// crumbPill: rounded clickable pill with a badge, text and an optional
// dropdown arrow — the toolbar connection breadcrumb and database selector
// (TablePlus style).

type crumbPill struct {
	widget.BaseWidget
	badge fyne.CanvasObject
	text  *canvas.Text
	arrow fyne.Resource // nil = no dropdown arrow
	onTap func()
	hover bool
}

func newCrumbPill(badge fyne.CanvasObject, text string, arrow fyne.Resource, onTap func()) *crumbPill {
	c := &crumbPill{badge: badge, arrow: arrow, onTap: onTap}
	c.text = canvas.NewText(text, pal().foreground)
	c.text.TextSize = 13
	c.text.TextStyle.Bold = true
	c.ExtendBaseWidget(c)
	return c
}

func (c *crumbPill) setText(s string) {
	c.text.Text = s
	c.Refresh()
	// Grow/shrink to fit the new text — parent layouts don't re-run on
	// their own when this pill changes size.
	c.Resize(c.MinSize())
}

func (c *crumbPill) Tapped(*fyne.PointEvent) {
	if c.onTap != nil {
		c.onTap()
	}
}

func (c *crumbPill) Cursor() desktop.Cursor { return desktop.DefaultCursor }

func (c *crumbPill) MouseIn(*desktop.MouseEvent) {
	c.hover = true
	c.Refresh()
}

func (c *crumbPill) MouseOut() {
	c.hover = false
	c.Refresh()
}

func (c *crumbPill) MouseMoved(*desktop.MouseEvent) {}

func (c *crumbPill) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(pal().crumbBG)
	bg.CornerRadius = 8
	line := canvas.NewRectangle(pal().crumbBorder)
	line.CornerRadius = 8
	line.StrokeWidth = 1
	var arrow *widget.Icon
	if c.arrow != nil {
		arrow = widget.NewIcon(c.arrow)
	}
	return &crumbPillRenderer{c: c, bg: bg, line: line, arrow: arrow}
}

type crumbPillRenderer struct {
	c     *crumbPill
	bg    *canvas.Rectangle
	line  *canvas.Rectangle
	arrow *widget.Icon
	size  fyne.Size
}

func (r *crumbPillRenderer) Objects() []fyne.CanvasObject {
	objs := []fyne.CanvasObject{r.bg, r.line, r.c.badge, r.c.text}
	if r.arrow != nil {
		objs = append(objs, r.arrow)
	}
	return objs
}

func (r *crumbPillRenderer) MinSize() fyne.Size {
	// badge (8 + 18) + gap 6 + text + right padding 8; the dropdown arrow
	// adds 16 + 8.
	w := r.c.text.MinSize().Width + 40
	if r.arrow != nil {
		w += 24
	}
	return fyne.NewSize(w, 30)
}

func (r *crumbPillRenderer) Destroy() {}

func (r *crumbPillRenderer) Layout(size fyne.Size) {
	r.size = size
	r.bg.Resize(size)
	r.line.Resize(size)
	r.c.badge.Resize(fyne.NewSize(18, 18))
	r.c.badge.Move(fyne.NewPos(8, (size.Height-18)/2))
	t := r.c.text.MinSize()
	r.c.text.Move(fyne.NewPos(32, (size.Height-t.Height)/2))
	if r.arrow != nil {
		r.arrow.Resize(fyne.NewSize(16, 16))
		r.arrow.Move(fyne.NewPos(size.Width-24, (size.Height-16)/2))
	}
}

func (r *crumbPillRenderer) Refresh() {
	p := pal()
	r.bg.FillColor = p.crumbBG
	if r.c.hover {
		r.bg.FillColor = p.hover
	}
	r.line.StrokeColor = p.crumbBorder
	r.c.text.Color = p.foreground
	r.bg.Refresh()
	r.line.Refresh()
	r.c.text.Refresh()
	// re-run the layout so a changed text/arrow keeps its position
	if r.size.Width > 0 || r.size.Height > 0 {
		r.Layout(r.size)
	}
}

// ---------------------------------------------------------------------------
// serverRibbon: the green server-info bar at the top of the workspace,
// mirroring TablePlus's connection ribbon
// ("LOCAL | MySQL 5.7.40 : TLSv1.2 : host : db : table"). Segments whose value
// is empty are omitted; tapping the bar opens the connection picker.

type serverRibbon struct {
	widget.BaseWidget
	env        string
	server     string
	tls        string
	host       string
	db         string
	table      string
	onTap      func()
	bg         *canvas.Rectangle
	text       *canvas.Text
	bgOverride color.Color // connection status colour; zero = use palette default
}

func newServerRibbon() *serverRibbon {
	r := &serverRibbon{}
	r.bg = canvas.NewRectangle(pal().ribbonBG)
	r.bg.CornerRadius = 8
	r.text = canvas.NewText("", pal().ribbonFG)
	r.text.TextSize = 13
	r.text.TextStyle.Bold = true
	r.ExtendBaseWidget(r)
	return r
}

// label joins the non-empty segments: "ENV | a : b : c".
func (r *serverRibbon) label() string {
	var rest []string
	for _, s := range []string{r.server, r.tls, r.host, r.db, r.table} {
		if s != "" {
			rest = append(rest, s)
		}
	}
	tail := strings.Join(rest, " : ")
	switch {
	case r.env == "":
		return tail
	case tail == "":
		return r.env
	default:
		return r.env + " | " + tail
	}
}

// update swaps the segment values and re-renders, growing to fit the text.
func (r *serverRibbon) update(env, server, tls, host, db, table string) {
	r.env, r.server, r.tls, r.host, r.db, r.table = env, server, tls, host, db, table
	r.text.Text = r.label()
	r.Resize(r.MinSize())
	r.Refresh()
}

func (r *serverRibbon) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap()
	}
}

func (r *serverRibbon) Cursor() desktop.Cursor { return desktop.DefaultCursor }

func (r *serverRibbon) CreateRenderer() fyne.WidgetRenderer {
	return &serverRibbonRenderer{r: r}
}

type serverRibbonRenderer struct{ r *serverRibbon }

func (rd *serverRibbonRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{rd.r.bg, rd.r.text}
}

func (rd *serverRibbonRenderer) MinSize() fyne.Size {
	return fyne.NewSize(rd.r.text.MinSize().Width+40, 30)
}

func (rd *serverRibbonRenderer) Destroy() {}

func (rd *serverRibbonRenderer) Layout(size fyne.Size) {
	rd.r.bg.Resize(size)
	t := rd.r.text.MinSize()
	rd.r.text.Move(fyne.NewPos(16, (size.Height-t.Height)/2))
}

// SetStatusColor overrides the ribbon's background with a connection status
// colour (TablePlus "Status Color"); pass nil to fall back to the palette.
func (r *serverRibbon) SetStatusColor(c color.Color) {
	r.bgOverride = c
	r.Refresh()
}

func (rd *serverRibbonRenderer) Refresh() {
	p := pal()
	rd.r.text.Text = rd.r.label()
	if rd.r.bgOverride != nil {
		rd.r.bg.FillColor = rd.r.bgOverride
	} else {
		rd.r.bg.FillColor = p.ribbonBG
	}
	rd.r.text.Color = p.ribbonFG
	rd.r.bg.Refresh()
	rd.r.text.Refresh()
}

// ---------------------------------------------------------------------------
// groupHeader: a clickable, collapsible group title (▸/▾ + name) for the
// welcome page's connection list.

type groupHeader struct {
	widget.BaseWidget
	title    string
	open     bool
	onToggle func(bool)
	hover    bool
}

func newGroupHeader(title string, open bool, onToggle func(bool)) *groupHeader {
	g := &groupHeader{title: title, open: open, onToggle: onToggle}
	g.ExtendBaseWidget(g)
	return g
}

func (g *groupHeader) Tapped(*fyne.PointEvent) {
	g.open = !g.open
	g.Refresh()
	if g.onToggle != nil {
		g.onToggle(g.open)
	}
}

func (g *groupHeader) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (g *groupHeader) MouseIn(*desktop.MouseEvent)    { g.hover = true; g.Refresh() }
func (g *groupHeader) MouseOut()                      { g.hover = false; g.Refresh() }
func (g *groupHeader) MouseMoved(*desktop.MouseEvent) {}

func (g *groupHeader) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(colorTransparent)
	tri := canvas.NewText("", pal().welcomeFG)
	tri.TextSize = 11
	title := canvas.NewText(g.title, pal().foreground)
	title.TextSize = 13
	title.TextStyle.Bold = true
	return &groupHeaderRenderer{g: g, bg: bg, tri: tri, title: title}
}

type groupHeaderRenderer struct {
	g     *groupHeader
	bg    *canvas.Rectangle
	tri   *canvas.Text
	title *canvas.Text
}

func (r *groupHeaderRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.tri, r.title}
}

func (r *groupHeaderRenderer) MinSize() fyne.Size {
	return fyne.NewSize(r.title.MinSize().Width+40, 28)
}

func (r *groupHeaderRenderer) Destroy() {}

func (r *groupHeaderRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.tri.Move(fyne.NewPos(0, (size.Height-r.tri.MinSize().Height)/2))
	r.title.Move(fyne.NewPos(18, (size.Height-r.title.MinSize().Height)/2))
}

func (r *groupHeaderRenderer) Refresh() {
	if r.g.open {
		r.tri.Text = "▼"
	} else {
		r.tri.Text = "▶"
	}
	r.tri.Color = pal().welcomeFG
	r.title.Color = pal().foreground
	r.title.Text = r.g.title
	if r.g.hover {
		r.bg.FillColor = pal().hover
	} else {
		r.bg.FillColor = colorTransparent
	}
	r.bg.Refresh()
	r.tri.Refresh()
	r.title.Refresh()
}

// ---------------------------------------------------------------------------
// statusBar: the bottom bar's row-range label, TablePlus style
// ("1-300 of 36,796 行数"), centered.

type statusBar struct {
	widget.BaseWidget
	from      int // first displayed row (1-based)
	displayed int
	total     int
	approx    bool // total is an estimate ("~N")
	query     bool // query-result mode: left-aligned "● ..." line
	queryText string
}

func newStatusBar() *statusBar {
	s := &statusBar{}
	s.ExtendBaseWidget(s)
	return s
}

func (s *statusBar) setRows(displayed, total int, approx bool) {
	s.displayed = displayed
	s.total = total
	s.approx = approx
	s.query = false
	s.Refresh()
}

// setFrom sets the 1-based index of the first displayed row (paging).
func (s *statusBar) setFrom(from int) {
	s.from = from
	s.Refresh()
}

func (s *statusBar) setTable(name string) {}

func (s *statusBar) setColumns(n int) {}

// setQueryInfo switches the bar into query-result mode and shows text after a
// leading status dot, TablePlus style ("● Query result — 3 columns · 20 rows").
func (s *statusBar) setQueryInfo(text string) {
	s.query = true
	s.queryText = text
	s.Refresh()
}

func (s *statusBar) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(pal().statusBG)
	dot := canvas.NewCircle(pal().okDot)
	label := canvas.NewText("", pal().statusFG)
	label.TextSize = 12
	return &statusBarRenderer{s: s, bg: bg, dot: dot, label: label}
}

type statusBarRenderer struct {
	s     *statusBar
	bg    *canvas.Rectangle
	dot   *canvas.Circle
	label *canvas.Text
}

func (r *statusBarRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.dot, r.label}
}

func (r *statusBarRenderer) MinSize() fyne.Size { return fyne.NewSize(200, 26) }

func (r *statusBarRenderer) Destroy() {}

func (r *statusBarRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	l := r.label.MinSize()
	if r.s.query {
		dy := (size.Height - 8) / 2
		r.dot.Resize(fyne.NewSize(8, 8))
		r.dot.Move(fyne.NewPos(12, dy))
		r.label.Move(fyne.NewPos(26, (size.Height-l.Height)/2))
	} else {
		r.dot.Resize(fyne.NewSize(0, 0))
		r.label.Move(fyne.NewPos((size.Width-l.Width)/2, (size.Height-l.Height)/2))
	}
}

func (r *statusBarRenderer) Refresh() {
	p := pal()
	r.bg.FillColor = p.statusBG
	r.label.Color = p.statusFG
	r.dot.FillColor = p.okDot
	if r.s.query {
		r.label.Text = r.s.queryText
	} else if r.s.displayed <= 0 {
		r.label.Text = ""
	} else {
		total := humanNum(r.s.total)
		if r.s.approx {
			total = "~" + total
		}
		from := r.s.from
		if from < 1 {
			from = 1
		}
		r.label.Text = fmt.Sprintf("%d-%d of %s rows", from, from+r.s.displayed-1, total)
	}
	for _, o := range r.Objects() {
		if c, ok := o.(interface{ Refresh() }); ok {
			c.Refresh()
		}
	}
	r.Layout(r.bg.Size())
}

func humanNum(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%d,%03d", n/1000, n%1000)
}
