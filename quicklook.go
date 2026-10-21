package main

// Quick Look (Beekeeper Studio "Quick Look"): an enlarged viewer for one
// cell's value — long text, JSON (tree + pretty/minify) or binary data
// (hex view). The view is auto-picked from the value's shape; the tab row
// lets the user override it.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
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
		return container.NewPadded(jsonTree(v))
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

	// --- view tab row (only the available views) ---------------------------
	tabs := container.NewHBox()
	for _, v := range views {
		name := v
		btn := newSideModeButton(viewTitle(v), func() {
			current = name
			rebuild()
			for _, o := range tabs.Objects {
				if mb, ok := o.(*sideModeButton); ok {
					mb.setActive(mb.label == viewTitle(name))
				}
			}
		})
		btn.setActive(name == current)
		tabs.Objects = append(tabs.Objects, container.NewPadded(btn))
	}

	copyBtn := newFlatButton("Copy value")
	copyBtn.onTap = func() {
		fyne.CurrentApp().Clipboard().SetContent(raw)
	}

	var dlg dialog.Dialog
	dlg = dialog.NewCustom(title, "", container.NewBorder(
		tabs,
		container.NewHBox(container.NewPadded(copyBtn)),
		nil, nil,
		container.NewPadded(slot),
	), t.window)
	dlg.Resize(fyne.NewSize(560, 420))
	dlg.Show()
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

// jsonTree renders a parsed JSON value as a collapsible Fyne tree.
func jsonTree(v any) *widget.Tree {
	// Precompute every reachable node's key path.
	nodes := map[string]any{".": v}
	var walk func(path string, node any)
	walk = func(path string, node any) {
		switch n := node.(type) {
		case map[string]any:
			for _, k := range sortedKeys(n) {
				np := path + k
				if _, seen := nodes[np]; !seen {
					nodes[np] = n[k]
					walk(np, n[k])
				}
			}
		case []any:
			for i, c := range n {
				np := path + "[" + strconv.Itoa(i) + "]"
				if _, seen := nodes[np]; !seen {
					nodes[np] = c
					walk(np, c)
				}
			}
		}
	}
	walk(".", v)

	childKeys := func(path string) []string {
		switch n := nodes[path].(type) {
		case map[string]any:
			out := make([]string, 0, len(n))
			for k := range n {
				out = append(out, path+k)
			}
			return out
		case []any:
			out := make([]string, len(n))
			for i := range n {
				out[i] = path + "[" + strconv.Itoa(i) + "]"
			}
			return out
		}
		return nil
	}
	isBranch := func(path string) bool {
		switch nodes[path].(type) {
		case map[string]any, []any:
			return true
		}
		return false
	}
	// pathKey returns the leaf label (the map key or "[i]") for a path.
	pathLabel := func(path string) string {
		if path == "." {
			return "root"
		}
		switch n := nodes[path].(type) {
		case map[string]any:
			_ = n
		}
		if i := strings.LastIndex(path, "["); i >= 0 {
			return path[i:]
		}
		parent, key := splitPath(path)
		_ = parent
		return key
	}

	// Label: "key" for branches, "key: preview" for leaves.
	labelFor := func(path string) string {
		name := pathLabel(path)
		if isBranch(path) {
			return name
		}
		preview := ""
		switch v2 := nodes[path].(type) {
		case string:
			preview = strconv.Quote(v2)
			if len(preview) > 40 {
				preview = preview[:40] + "…"
			}
		case nil:
			preview = "null"
		default:
			preview = fmt.Sprint(v2)
			if len(preview) > 40 {
				preview = preview[:40] + "…"
			}
		}
		return name + ": " + preview
	}

	create := func(bool) fyne.CanvasObject { return widget.NewLabel("node") }
	update := func(id string, _ bool, obj fyne.CanvasObject) {
		obj.(*widget.Label).SetText(labelFor(id))
	}
	return widget.NewTree(childKeys, isBranch, create, update)
}

// splitPath returns (parentPath, key) for a map-key path like "a.b".
func splitPath(path string) (string, string) {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i], path[i+1:]
	}
	return ".", path
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
