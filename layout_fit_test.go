package main

// The panes stay inside the window.
//
// A Fyne canvas lays out by the rules of its containers, and a widget that
// wants more room than it is given does not scroll: it is drawn past the edge
// and the part is lost. Nothing tells the user — the pane is simply shorter
// than it should be, with its last row cut off.
//
// This measures the two widgets that sit at the edge of the window: the state
// line at the bottom and the grid in the middle. Fyne's test window reports
// every object's size and position, so "is it inside the window" is a
// measurement rather than a judgement.
//
// It is the same question dbstudio had to answer twice (WORKSPACE §80: the
// tree's rows were laid out past the window; §83: they painted over the state
// line). Here the layout is Fyne's, so the answer is measured on Fyne's terms.
//
// 波 101 续三十九.

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/test"
)

// TestStateLineStaysAtTheBottom lays the state line out the way the workspace
// holds it — at the bottom of a border, with the content above — and checks
// that it is where a state line belongs: at the bottom, inside the window, and
// no taller than the one line it asks for.
func TestStateLineStaysAtTheBottom(t *testing.T) {
	test.NewApp()
	bar := newStatusBar()
	bar.setRows(120, 5000, false)
	bar.setColumns(7)
	bar.setTable("erp_price")

	w := test.NewWindow(container.NewBorder(nil, bar, nil, nil, layout.NewSpacer()))
	defer w.Close()
	w.Resize(fyne.NewSize(900, 600))

	pos := bar.Position()
	size := bar.Size()
	if pos.Y+size.Height > 600 {
		t.Errorf("the state line ends at %.0f, below the window's 600", pos.Y+size.Height)
	}
	if size.Height <= 0 {
		t.Errorf("the state line has no height (%.0f)", size.Height)
	}
	// A state line is one line; a taller one means the layout gave it more
	// room than it asked for.
	if size.Height > 40 {
		t.Errorf("the state line is %.0f tall, which is more than one line", size.Height)
	}
	// It sits at the bottom: the space below it is the border's own padding,
	// which is the breathing room the layout gives every pane — a few pixels,
	// not a strip of empty window.
	if gap := 600 - (pos.Y + size.Height); gap > 12 {
		t.Errorf("the state line ends %.0f above the window's bottom, so it is not at the bottom", gap)
	}
}

// TestGridStaysInsideTheWindow lays the grid out in a window and checks that
// it is neither wider nor taller than the window it is in.
func TestGridStaysInsideTheWindow(t *testing.T) {
	test.NewApp()
	status := newStatusBar()
	data := &TableData{
		Cols: []ColumnInfo{
			{Name: "id", Type: "int"},
			{Name: "name", Type: "text"},
			{Name: "客户备注", Type: "text"},
		},
		Rows: [][]*string{
			{str("1"), str("alice"), str("created")},
			{str("2"), str("客户"), str("updated")},
		},
		Total: 2, Fetched: 2,
	}
	grid := newDataTable(data, status)

	w := test.NewWindow(container.NewBorder(nil, status, nil, nil, grid))
	defer w.Close()
	w.Resize(fyne.NewSize(900, 600))

	size := grid.Size()
	if size.Width > 900 {
		t.Errorf("the grid is %.0f wide, wider than the window's 900", size.Width)
	}
	if size.Height > 600 {
		t.Errorf("the grid is %.0f tall, taller than the window's 600", size.Height)
	}
}

// TestStateLineAndGridShareTheWindow is the pair the window really holds: the
// grid above and the state line below it. They must not overlap, and the
// state line must not be pushed off the bottom.
func TestStateLineAndGridShareTheWindow(t *testing.T) {
	test.NewApp()
	bar := newStatusBar()
	bar.setRows(120, 5000, false)
	data := &TableData{
		Cols:  []ColumnInfo{{Name: "id", Type: "int"}},
		Rows:  [][]*string{{str("1")}},
		Total: 1, Fetched: 1,
	}
	grid := newDataTable(data, bar)

	w := test.NewWindow(container.NewBorder(nil, bar, nil, nil, grid))
	defer w.Close()
	w.Resize(fyne.NewSize(900, 600))

	g := grid.Position()
	gSize := grid.Size()
	b := bar.Position()
	bSize := bar.Size()

	if g.Y+gSize.Height > b.Y+0.5 {
		t.Errorf("the grid ends at %.0f and the state line starts at %.0f — they overlap",
			g.Y+gSize.Height, b.Y)
	}
	if b.Y+bSize.Height > 600 {
		t.Errorf("the state line ends at %.0f, below the window's 600", b.Y+bSize.Height)
	}
}
