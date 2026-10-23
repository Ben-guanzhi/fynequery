package main

// Tests for the Quick Look JSON tree model. Fyne's Tree roots at the empty id
// "", so childKeys("") must return the top-level nodes — the original model
// keyed the root as "." and rendered an empty tree.

import (
	"encoding/json"
	"fmt"
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func parseJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func TestJSONTreeModelRoot(t *testing.T) {
	m := buildJSONTreeModel(parseJSON(t, `{"b":2,"a":{"x":1},"c":[10,20]}`))

	// Root children, sorted: a, b, c.
	root := m.children("")
	if len(root) != 3 {
		t.Fatalf("root children = %v, want 3", root)
	}
	// The "a" branch expands to its key.
	var aID string
	for _, id := range root {
		if m.label(id) == "a  {1}" {
			aID = id
		}
	}
	if aID == "" {
		t.Fatalf("no 'a' branch among %v (labels: %q %q %q)", root, m.label(root[0]), m.label(root[1]), m.label(root[2]))
	}
	if !m.branch(aID) {
		t.Fatal("'a' should be a branch")
	}
	aKids := m.children(aID)
	if len(aKids) != 1 || m.label(aKids[0]) != `x: 1` {
		t.Fatalf("a.x = %v", aKids)
	}

	// The array branch labels its elements by index.
	var cID string
	for _, id := range root {
		if m.label(id) == "c  [2]" {
			cID = id
		}
	}
	if cID == "" {
		t.Fatal("no 'c' array branch")
	}
	cKids := m.children(cID)
	if len(cKids) != 2 || m.label(cKids[0]) != "[0]: 10" || m.label(cKids[1]) != "[1]: 20" {
		t.Fatalf("c elements = %v", cKids)
	}
}

func TestJSONTreeModelScalarRoot(t *testing.T) {
	// A scalar top-level value has no children and is not a branch.
	m := buildJSONTreeModel(parseJSON(t, `42`))
	if len(m.children("")) != 0 {
		t.Fatalf("scalar root should have no children, got %v", m.children(""))
	}
	if m.branch("") {
		t.Fatal("scalar root should not be a branch")
	}
	if m.label("") != "root: 42" {
		t.Fatalf("scalar root label = %q", m.label(""))
	}
}

func TestJSONTreeModelKeysWithDots(t *testing.T) {
	// Keys containing the separator-ish characters must not collide or merge.
	m := buildJSONTreeModel(parseJSON(t, `{"a.b":1,"a":{"b":2}}`))
	root := m.children("")
	if len(root) != 2 {
		t.Fatalf("root children = %d, want 2 (a.b and a)", len(root))
	}
	// One is the literal key "a.b" (leaf, value 1), the other is the object
	// "a" (branch). They must not collide or merge.
	var sawLeaf, sawBranch bool
	for _, id := range root {
		if m.label(id) == "a.b: 1" {
			sawLeaf = true
		}
		if m.label(id) == "a  {1}" {
			sawBranch = true
		}
	}
	if !sawLeaf || !sawBranch {
		t.Fatalf("expected both a.b leaf and a branch, got leaf=%v branch=%v", sawLeaf, sawBranch)
	}
}

// TestQuickLookLayoutBounds builds the popover layout with a large JSON tree
// and asserts the content's MinSize stays within the popover's fixed size — a
// regression guard against the tree inflating/overflowing the popover border.
func TestQuickLookLayoutBounds(t *testing.T) {
	// A wide + deep JSON so the tree's natural (content) size is large.
	big := map[string]any{}
	for i := 0; i < 200; i++ {
		row := map[string]any{}
		for j := 0; j < 8; j++ {
			row[fmt.Sprintf("key_%02d_with_a_fairly_long_name", j)] = "a somewhat long string value that widens the tree"
		}
		big[fmt.Sprintf("item_%03d", i)] = row
	}

	treeSlot := container.NewStack()
	treeSlot.Objects = []fyne.CanvasObject{jsonTree(big)}

	tabs := container.NewHBox(
		container.NewPadded(newSideModeButton("JSON Tree", nil)),
		container.NewPadded(newSideModeButton("Text", nil)),
		container.NewPadded(newSideModeButton("Hex", nil)),
	)
	header := container.NewBorder(nil, nil, widget.NewLabel("Quick Look — x"), container.NewPadded(newFlatButton("Close")), nil)
	footer := container.NewHBox(widget.NewLabel(""), container.NewPadded(newFlatButton("Copy value")))
	body := container.NewBorder(header, footer, nil, nil,
		container.NewBorder(tabs, nil, nil, nil, container.NewPadded(treeSlot)))
	card := newRoundedPanel(body,
		func() color.Color { return color.Transparent },
		func() color.Color { return color.Transparent },
		10, 1)

	const popW, popH = 620, 460
	if m := card.MinSize(); m.Width > popW || m.Height > popH {
		t.Fatalf("quick-look content MinSize %v exceeds popover %dx%d — the tree would overflow the border", m, popW, popH)
	}
}

// TestQuickLookPopoverSize creates the real PopUp with a large tree and checks
// the popover keeps its fixed size and the card does not overflow it.
func TestQuickLookPopoverSize(t *testing.T) {
	test.NewApp()
	w := test.NewWindow(widget.NewLabel("host"))
	w.Resize(fyne.NewSize(1000, 800))
	defer w.Close()
	c := w.Canvas()

	big := map[string]any{}
	for i := 0; i < 300; i++ {
		row := map[string]any{}
		for j := 0; j < 6; j++ {
			row[fmt.Sprintf("k%02d_long_key_name", j)] = "long value string to widen rows"
		}
		big[fmt.Sprintf("item_%03d", i)] = row
	}
	card := newRoundedPanel(jsonTree(big),
		func() color.Color { return color.Transparent },
		func() color.Color { return color.Transparent },
		10, 1)

	pop := widget.NewPopUp(card, c)
	pop.Resize(fyne.NewSize(620, 460))
	pop.Show()

	if sz := pop.Size(); sz.Width != 620 || sz.Height != 460 {
		t.Fatalf("popup size = %v, want 620x460", sz)
	}
	if sz := card.Size(); sz.Width > 620 || sz.Height > 460 {
		t.Fatalf("card size %v overflows popup 620x460", sz)
	}
	pop.Hide()
}

func TestFlattenJSONTreeExpandCollapse(t *testing.T) {
	m := buildJSONTreeModel(parseJSON(t, `{"a":{"x":1,"y":2},"b":3}`))

	// Fully expanded: a (branch), a.x, a.y, b — in DFS order.
	rows := flattenJSONTree(m, nil)
	if len(rows) != 4 {
		t.Fatalf("expanded rows = %d (%v), want 4", len(rows), labelsOf(rows))
	}
	if rows[0].label != "a  {2}" || !rows[0].branch || rows[0].depth != 0 {
		t.Fatalf("row0 = %+v", rows[0])
	}
	if rows[1].label != "x: 1" || rows[1].depth != 1 {
		t.Fatalf("row1 = %+v", rows[1])
	}
	if rows[3].label != "b: 3" || rows[3].depth != 0 {
		t.Fatalf("row3 = %+v", rows[3])
	}

	// Collapse "a": its subtree disappears, leaving a and b.
	var aID string
	for _, r := range rows {
		if r.branch && r.depth == 0 && r.label == "a  {2}" {
			aID = r.id
		}
	}
	collapsed := flattenJSONTree(m, map[string]bool{aID: true})
	if len(collapsed) != 2 {
		t.Fatalf("collapsed rows = %d (%v), want 2", len(collapsed), labelsOf(collapsed))
	}
	if collapsed[0].expanded {
		t.Fatal("collapsed branch should report expanded=false")
	}
	if collapsed[1].label != "b: 3" {
		t.Fatalf("after collapse row1 = %+v", collapsed[1])
	}
}

func TestFlattenJSONTreeScalarRoot(t *testing.T) {
	m := buildJSONTreeModel(parseJSON(t, `"just a string"`))
	rows := flattenJSONTree(m, nil)
	if len(rows) != 1 || rows[0].branch {
		t.Fatalf("scalar root rows = %+v, want 1 non-branch", rows)
	}
}

func labelsOf(rows []jsonRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.label
	}
	return out
}
