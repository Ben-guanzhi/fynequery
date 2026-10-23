package main

// Quick Look (Beekeeper Studio "Quick Look"): an enlarged viewer for one
// cell's value — long text, JSON (tree + pretty/minify) or binary data
// (hex view). The view is auto-picked from the value's shape; the tab row
// lets the user override it.

import (
	"encoding/json"
	"fmt"
	"image/color"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// quickLook opens the enlarged viewer for one grid cell's value.
func (t *dataTable) quickLook(row, col int) {
	if t.window == nil {
		return
	}
	raw := ""
	if v := cellStr(t.data.Rows[row], col); v != nil {
		raw = *v
	}
	colName := ""
	if col >= 0 && col < len(t.data.Cols) {
		colName = t.colTitle(col)
	}

	isJSON := json.Valid([]byte(raw))
	isBinary := looksBinary(raw)
	defaultView := "text"
	if isJSON {
		defaultView = "tree"
	} else if isBinary {
		defaultView = "hex"
	}

	views := []string{"text", "hex"}
	if isJSON {
		views = []string{"tree", "text", "hex"}
	}
	current := defaultView
	slots := []string{"text", "hex"}
	if isJSON {
		slots = append([]string{"tree"}, slots...)
	}

	title := "Quick Look"
	if colName != "" {
		title += " — " + colName
	}

	// --- view builders -----------------------------------------------------
	makeTextView := func() fyne.CanvasObject {
		e := widget.NewMultiLineEntry()
		e.TextStyle.Monospace = true
		e.Wrapping = fyne.TextWrapOff
		e.SetText(raw)
		var text fyne.CanvasObject = container.NewScroll(container.NewPadded(e))
		if isJSON {
			pretty := newFlatButton("Pretty")
			minify := newFlatButton("Minify")
			pretty.onTap = func() { fmtJSON(raw, e) }
			minify.onTap = func() {
				var v any
				if err := json.Unmarshal([]byte(raw), &v); err == nil {
					if b, err2 := json.Marshal(v); err2 == nil {
						e.SetText(string(b))
					}
				}
			}
			text = container.NewVBox(
				container.NewHBox(container.NewPadded(pretty), container.NewPadded(minify)),
				container.NewScroll(container.NewPadded(e)),
			)
		}
		return text
	}
	hexView := func() fyne.CanvasObject {
		return container.NewScroll(container.NewPadded(hexViewLabel(raw)))
	}
	treeView := func() fyne.CanvasObject {
		var v any
		_ = json.Unmarshal([]byte(raw), &v)
		// jsonTree is a self-scrolling virtualized list. The slot is already
		// padded by the body border, so don't pad again (double padding pushed
		// the whole tree right, making root keys look one level deep).
		return jsonTree(v)
	}

	slot := container.NewStack()
	rebuild := func() {
		switch current {
		case "tree":
			slot.Objects = []fyne.CanvasObject{treeView()}
		case "hex":
			slot.Objects = []fyne.CanvasObject{hexView()}
		default:
			slot.Objects = []fyne.CanvasObject{makeTextView()}
		}
		slot.Refresh()
	}
	rebuild()

	// --- view selector (dropdown of the available views) ------------------
	titles := make([]string, 0, len(views))
	titleToView := make(map[string]string, len(views))
	for _, v := range views {
		t := viewTitle(v)
		titles = append(titles, t)
		titleToView[t] = v
	}
	viewSelect := widget.NewSelect(titles, func(t string) {
		current = titleToView[t]
		rebuild()
	})
	viewSelect.SetSelected(viewTitle(current))
	tabs := viewSelect

	copyBtn := newFlatButton("Copy value")
	copyBtn.onTap = func() {
		fyne.CurrentApp().Clipboard().SetContent(raw)
	}
	closeBtn := newFlatButton("Close")

	// Non-modal popover: unlike a dialog it does not dim/block the workspace,
	// so the value can be inspected while the grid stays usable (TablePlus
	// "Quick Look" feel). It floats over the grid's canvas and closes via the
	// Close button.
	titleLbl := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	header := container.NewBorder(nil, nil, titleLbl, container.NewPadded(closeBtn), nil)
	footer := container.NewHBox(layout.NewSpacer(), container.NewPadded(copyBtn))
	body := container.NewBorder(
		header,
		footer,
		nil, nil,
		container.NewBorder(tabs, nil, nil, nil, container.NewPadded(slot)),
	)
	card := newRoundedPanel(body,
		func() color.Color { return pal().contentBG },
		func() color.Color { return pal().crumbBorder },
		10, 1)

	canvas := fyne.CurrentApp().Driver().CanvasForObject(t)
	if canvas == nil {
		return
	}
	pop := widget.NewPopUp(card, canvas)
	closeBtn.onTap = func() { pop.Hide() }
	size := fyne.NewSize(620, 460)
	pop.Resize(size)
	pop.Move(fyne.NewPos(
		(canvas.Size().Width-size.Width)/2,
		(canvas.Size().Height-size.Height)/2,
	))
	pop.Show()
}

func viewTitle(v string) string {
	switch v {
	case "tree":
		return "JSON Tree"
	case "hex":
		return "Hex"
	default:
		return "Text"
	}
}

// looksBinary reports whether raw is binary-ish (NUL bytes or invalid UTF-8)
// and deserves a hex view by default.
func looksBinary(raw string) bool {
	if raw == "" {
		return false
	}
	if strings.ContainsRune(raw, 0) {
		return true
	}
	return !utf8.ValidString(raw)
}

// hexViewLabel renders raw as a classic hex dump (16 bytes/line + ASCII).
func hexViewLabel(raw string) *widget.Label {
	b := []byte(raw)
	const per = 16
	var out strings.Builder
	for off := 0; off < len(b); off += per {
		end := off + per
		if end > len(b) {
			end = len(b)
		}
		chunk := b[off:end]
		hexCol, ascii := "", ""
		for i, c := range chunk {
			hexCol += fmt.Sprintf("%02x ", c)
			if i+1 < len(chunk) && (i+1)%4 == 0 {
				hexCol += " "
			}
			if c >= 0x20 && c < 0x7f {
				ascii += string(c)
			} else {
				ascii += "."
			}
		}
		fmt.Fprintf(&out, "%08x  %-47s %s\n", off, hexCol, ascii)
	}
	if out.Len() == 0 {
		l2 := widget.NewLabelWithStyle("<empty>", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		l2.Wrapping = fyne.TextWrapOff
		return l2
	}
	l := widget.NewLabelWithStyle(out.String(), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	l.Wrapping = fyne.TextWrapOff
	return l
}

// jsonTreeModel is the node model behind the JSON tree view. Fyne's Tree roots
// at the empty id "", so node ids are "" for the root and NUL-delimited paths
// below it (a NUL separator keeps keys containing "." or "/" from colliding).
type jsonTreeModel struct {
	nodes    map[string]any
	children func(id string) []string
	branch   func(id string) bool
	label    func(id string) string
}

const jsonTreeSep = "\x00"

// buildJSONTreeModel indexes every reachable node of v by its path id.
func buildJSONTreeModel(v any) *jsonTreeModel {
	nodes := map[string]any{"": v}
	var walk func(path string, node any)
	walk = func(path string, node any) {
		switch n := node.(type) {
		case map[string]any:
			for _, k := range sortedKeys(n) {
				id := path + jsonTreeSep + k
				if _, seen := nodes[id]; !seen {
					nodes[id] = n[k]
					walk(id, n[k])
				}
			}
		case []any:
			for i, c := range n {
				id := path + jsonTreeSep + "[" + strconv.Itoa(i) + "]"
				if _, seen := nodes[id]; !seen {
					nodes[id] = c
					walk(id, c)
				}
			}
		}
	}
	walk("", v)

	children := func(id string) []string {
		switch n := nodes[id].(type) {
		case map[string]any:
			out := make([]string, 0, len(n))
			for _, k := range sortedKeys(n) {
				out = append(out, id+jsonTreeSep+k)
			}
			return out
		case []any:
			out := make([]string, len(n))
			for i := range n {
				out[i] = id + jsonTreeSep + "[" + strconv.Itoa(i) + "]"
			}
			return out
		}
		return nil
	}
	branch := func(id string) bool {
		switch nodes[id].(type) {
		case map[string]any, []any:
			return true
		}
		return false
	}
	leafName := func(id string) string {
		if id == "" {
			return "root"
		}
		if i := strings.LastIndex(id, jsonTreeSep); i >= 0 {
			return id[i+len(jsonTreeSep):]
		}
		return id
	}
	label := func(id string) string {
		name := leafName(id)
		if branch(id) {
			switch n := nodes[id].(type) {
			case map[string]any:
				return fmt.Sprintf("%s  {%d}", name, len(n))
			case []any:
				return fmt.Sprintf("%s  [%d]", name, len(n))
			}
		}
		preview := ""
		switch vv := nodes[id].(type) {
		case string:
			preview = strconv.Quote(vv)
		case nil:
			preview = "null"
		default:
			preview = fmt.Sprint(vv)
		}
		if len(preview) > 48 {
			preview = preview[:48] + "…"
		}
		return name + ": " + preview
	}
	return &jsonTreeModel{nodes: nodes, children: children, branch: branch, label: label}
}

// jsonRow is one visible row of the flattened JSON tree.
type jsonRow struct {
	id       string
	depth    int
	label    string
	branch   bool
	expanded bool
}

// flattenJSONTree walks the model in DFS order, skipping the children of
// collapsed branches, and returns the visible rows. The root ("") is not shown
// as a row; its children sit at depth 0 (a scalar root yields a single row).
func flattenJSONTree(m *jsonTreeModel, collapsed map[string]bool) []jsonRow {
	var rows []jsonRow
	if !m.branch("") {
		return append(rows, jsonRow{label: m.label("")})
	}
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		br := m.branch(id)
		exp := br && !collapsed[id]
		rows = append(rows, jsonRow{id: id, depth: depth, label: m.label(id), branch: br, expanded: exp})
		if exp {
			for _, c := range m.children(id) {
				walk(c, depth+1)
			}
		}
	}
	for _, c := range m.children("") {
		walk(c, 0)
	}
	return rows
}

// jsonTree renders a parsed JSON value as a virtualized, collapsible tree.
//
// It uses widget.List (not a VBox-in-Scroll): a List's MinSize is just its
// viewport, so the Quick Look popover stays at its fixed size and scrolls
// internally instead of the whole tree (and popover) growing past the border.
// Rows are single-line; long values are truncated to the available width (the
// full value is still in the "Text" tab). The arrow column is a fixed-size
// GridWrap cell so the label never shifts sideways; we use ASCII "+"/"-"
// carets (guaranteed one monospace cell) instead of ▾/▸, which are often
// font-substituted to a wider advance. A tap on a branch toggles it.
func jsonTree(v any) fyne.CanvasObject {
	m := buildJSONTreeModel(v)
	collapsed := map[string]bool{}
	var rows []jsonRow
	rebuild := func() { rows = flattenJSONTree(m, collapsed) }
	rebuild()

	// Per-depth indent (px) and a fixed-width arrow column.
	const indentUnit = 16
	monoChar := widget.NewLabel("M").MinSize().Width
	arrowCell := fyne.NewSize(
		widget.NewLabel("+").MinSize().Width, // one monospace cell — narrow & stable
		widget.NewLabel("M").MinSize().Height,
	)
	// Usable width inside the popover for a tree row. A fixed budget keeps
	// truncation stable regardless of when the List measures its own size
	// (it can be 0 during item creation, before layout assigns a width).
	const treeUsableWidth = 560

	list := widget.NewList(
		func() int { return len(rows) },
		func() fyne.CanvasObject {
			indent := canvas.NewRectangle(color.Transparent)
			arrow := widget.NewLabel("")
			arrow.TextStyle.Monospace = true
			arrow.Wrapping = fyne.TextWrapOff
			arrowBox := container.NewGridWrap(arrowCell, arrow)
			lbl := widget.NewLabel("")
			lbl.TextStyle.Monospace = true
			lbl.Wrapping = fyne.TextWrapOff
			return container.NewHBox(indent, arrowBox, lbl)
		},
		func(i int, obj fyne.CanvasObject) {
			r := rows[i]
			hb := obj.(*fyne.Container)
			indent := hb.Objects[0].(*canvas.Rectangle)
			arrowBox := hb.Objects[1].(*fyne.Container)
			lbl := hb.Objects[2].(*widget.Label)
			indent.SetMinSize(fyne.NewSize(float32(indentUnit*r.depth), 1))
			arrow := arrowBox.Objects[0].(*widget.Label)
			switch {
			case r.branch && r.expanded:
				arrow.SetText("-") // expanded
			case r.branch:
				arrow.SetText("+") // collapsed
			default:
				arrow.SetText("")
			}
			// Truncate so a single-line row never overflows the popup width.
			avail := treeUsableWidth - arrowCell.Width - float32(indentUnit*r.depth)
			maxRunes := int(avail / monoChar)
			if maxRunes < 4 {
				maxRunes = 4
			}
			lbl.SetText(truncateLabel(r.label, maxRunes))
			hb.Refresh() // re-layout after the indent MinSize change
		},
	)
	list.OnSelected = func(i int) {
		if i < 0 || i >= len(rows) {
			return
		}
		r := rows[i]
		if r.branch {
			if collapsed[r.id] {
				delete(collapsed, r.id)
			} else {
				collapsed[r.id] = true
			}
			rebuild()
			list.Refresh()
		}
		list.Unselect(i) // allow re-tap to toggle again
	}
	return list
}

// truncateLabel shortens s to at most maxRunes, appending an ellipsis.
func truncateLabel(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if maxRunes <= 1 {
		return string(r[:1])
	}
	return string(r[:maxRunes-1]) + "…"
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fmtJSON pretty-prints raw JSON into e (best effort).
func fmtJSON(raw string, e *widget.Entry) {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return
	}
	if b, err := json.MarshalIndent(v, "", "  "); err == nil {
		e.SetText(string(b))
	}
}
