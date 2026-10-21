package main

// SQL editor autocomplete. Fyne's widget.Entry has no built-in completion, so
// we implement a lightweight one: Ctrl+Space (or the Complete button) pops a
// menu of SQL keywords, functions and identifiers (tables/columns) filtered by
// the word under the cursor; picking one inserts it in place of that word.

import (
	"image/color"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// sqlKeywords are the statement/clause words offered by completion.
var sqlKeywords = []string{
	"ADD", "ALL", "ALTER", "AND", "AS", "ASC", "BEGIN", "BETWEEN", "BY",
	"CASCADE", "CASE", "CHECK", "COLUMN", "COMMIT", "CONSTRAINT", "CREATE",
	"CROSS", "DATABASE", "DEFAULT", "DELETE", "DESC", "DISTINCT", "DROP",
	"ELSE", "END", "EXISTS", "FOREIGN", "FROM", "FULL", "GROUP", "HAVING",
	"IF", "IN", "INDEX", "INNER", "INSERT", "INTO", "IS", "JOIN", "KEY",
	"LEFT", "LIKE", "LIMIT", "NOT", "NULL", "OFFSET", "ON", "OR", "ORDER",
	"OUTER", "PRIMARY", "REFERENCES", "RIGHT", "ROLLBACK", "SELECT", "SET",
	"TABLE", "THEN", "TRUNCATE", "UNION", "UNIQUE", "UPDATE", "VALUES",
	"VIEW", "WHEN", "WHERE",
}

// sqlFunctions are the built-in function names offered by completion.
var sqlFunctions = []string{
	"ABS", "AVG", "CAST", "CEIL", "COALESCE", "CONCAT", "COUNT", "CURDATE",
	"DATE", "DATEDIFF", "DATE_FORMAT", "DAY", "FLOOR", "GROUP_CONCAT", "IFNULL",
	"LENGTH", "LOWER", "LTRIM", "MAX", "MIN", "MONTH", "NOW", "NULLIF",
	"ROUND", "RTRIM", "SUBSTRING", "SUM", "TRIM", "UPPER", "YEAR",
}

// isWordRune reports whether r can be part of an identifier (letters, digits,
// underscore and the dot used for table.column references).
func isWordRune(r rune) bool {
	return r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// runeOffset converts a (row, column) cursor position into a rune offset.
func runeOffset(r []rune, row, col int) int {
	line, off := 0, 0
	for off < len(r) && line < row {
		if r[off] == '\n' {
			line++
		}
		off++
	}
	off += col
	if off > len(r) {
		off = len(r)
	}
	if off < 0 {
		off = 0
	}
	return off
}

// buildWords merges the keyword/function/identifier candidates into a sorted,
// de-duplicated list (case-insensitive).
func buildWords(tables, columns []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(words []string) {
		for _, w := range words {
			if w == "" {
				continue
			}
			key := strings.ToLower(w)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, w)
		}
	}
	add(sqlKeywords)
	add(sqlFunctions)
	add(tables)
	add(columns)
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i]), strings.ToLower(out[j])
		if a == b {
			return out[i] < out[j]
		}
		return a < b
	})
	return out
}

// matchWords returns the words starting with prefix (case-insensitive), up to
// limit entries. An empty prefix returns everything.
func matchWords(words []string, prefix string, limit int) []string {
	p := strings.ToLower(prefix)
	var out []string
	for _, w := range words {
		if p != "" && !strings.HasPrefix(strings.ToLower(w), p) {
			continue
		}
		out = append(out, w)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// referencedTables returns the known table names that appear as words in the
// statement, so completion can offer their columns.
func referencedTables(sqlText string, known []string) []string {
	if len(known) == 0 {
		return nil
	}
	have := map[string]string{} // lowercased -> canonical
	for _, t := range known {
		have[strings.ToLower(t)] = t
	}
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.FieldsFunc(sqlText, func(r rune) bool { return !isWordRune(r) }) {
		if name, ok := have[strings.ToLower(tok)]; ok && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// ---- queryPane completion --------------------------------------------------

// currentWord returns the rune offset where the word under the cursor starts
// and the word text itself.
func (qp *queryPane) currentWord() (int, string) {
	if qp.editor == nil {
		return 0, ""
	}
	r := []rune(qp.editor.Text)
	off := runeOffset(r, qp.editor.CursorRow, qp.editor.CursorColumn)
	start := off
	for start > 0 && isWordRune(r[start-1]) {
		start--
	}
	return start, string(r[start:off])
}

// insertCompletion replaces the word at [start, start+wordLen) with ins and
// moves the cursor to just after the inserted text.
func (qp *queryPane) insertCompletion(start, wordLen int, ins string) {
	if qp.editor == nil {
		return
	}
	r := []rune(qp.editor.Text)
	if start < 0 {
		start = 0
	}
	if start > len(r) {
		start = len(r)
	}
	if start+wordLen > len(r) {
		wordLen = len(r) - start
	}
	out := string(r[:start]) + ins + string(r[start+wordLen:])
	qp.editor.SetText(out)
	qp.setCursorRune(start + len([]rune(ins)))
}

// setCursorRune places the entry cursor at the given rune offset.
func (qp *queryPane) setCursorRune(pos int) {
	if qp.editor == nil {
		return
	}
	r := []rune(qp.editor.Text)
	if pos > len(r) {
		pos = len(r)
	}
	line, col := 0, 0
	for i := 0; i < pos; i++ {
		if r[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	qp.editor.CursorRow = line
	qp.editor.CursorColumn = col
	qp.editor.Refresh()
}

// moveToByte places the entry cursor at a byte offset (used to mark the start
// of the statement Run Current is about to execute — Fyne v2.8 has no public
// selection-range API, so the cursor marks the "highlight").
func (qp *queryPane) moveToByte(byteOff int) {
	if qp.editor == nil {
		return
	}
	r := []rune(qp.editor.Text)
	runeOff, b := 0, 0
	for runeOff < len(r) && b < byteOff {
		b += len(string(r[runeOff]))
		runeOff++
	}
	if b != byteOff {
		runeOff++ // offset fell inside a multi-byte rune; clamp forward
	}
	qp.setCursorRune(runeOff)
	qp.refreshPos()
}

// tableCandidates lists the table/view names known to the sidebar.
func (qp *queryPane) tableCandidates() []string {
	if qp.side == nil {
		return nil
	}
	var out []string
	for _, o := range qp.side.objects {
		out = append(out, o.Name)
	}
	return out
}

// columnCandidates returns the columns of the given tables, consulting a cache
// and the session for the rest (best effort; errors are ignored).
func (qp *queryPane) columnCandidates(tables []string) []string {
	qp.colMu.Lock()
	defer qp.colMu.Unlock()
	if qp.colCache == nil {
		qp.colCache = map[string][]string{}
	}
	var out []string
	for _, t := range tables {
		cols, ok := qp.colCache[t]
		if !ok {
			if sess := qp.session(); sess != nil {
				if ci, err := sess.Columns(t); err == nil {
					for _, c := range ci {
						cols = append(cols, c.Name)
					}
				}
			}
			qp.colCache[t] = cols
		}
		out = append(out, cols...)
	}
	return out
}

// complete opens the completion menu for the word under the cursor. Besides
// keywords/functions/identifiers it offers keyword-bound saved queries
// (TablePlus "keyword binding"): typing a bound keyword offers the whole
// query, accepting it replaces the keyword with the full SQL.
func (qp *queryPane) complete() {
	if qp.editor == nil {
		return
	}
	qp.hideAuto()
	start, word := qp.currentWord()
	tables := qp.tableCandidates()
	refs := referencedTables(qp.editor.Text, tables)
	safeGo("query.complete", func() {
		cols := qp.columnCandidates(refs)
		words := buildWords(tables, cols)
		sug := matchWords(words, word, 80)
		kws := qp.queryKeywordSuggestions(word)
		safeDo(func() {
			if len(sug) == 0 && len(kws) == 0 {
				return
			}
			qp.showCompletionMenu(start, len([]rune(word)), sug, kws)
		})
	})
}

// queryKeywordHit is one keyword→query suggestion.
type queryKeywordHit struct {
	label string
	sql   string
}

// queryKeywordSuggestions lists the saved queries whose bound keyword
// prefix-matches the current word (case-insensitive; empty word lists all).
func (qp *queryPane) queryKeywordSuggestions(word string) []queryKeywordHit {
	p := strings.ToLower(strings.TrimSpace(word))
	var out []queryKeywordHit
	for kw, qs := range savedQueryKeywords() {
		if p != "" && !strings.HasPrefix(kw, p) {
			continue
		}
		for _, q := range qs {
			out = append(out, queryKeywordHit{label: "★ " + kw + " — " + q.Name, sql: q.SQL})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
	return out
}

// showCompletionMenu displays the suggestion popup next to the editor.
func (qp *queryPane) showCompletionMenu(start, wordLen int, suggestions []string, kws []queryKeywordHit) {
	if qp.window == nil {
		return
	}
	canvas := qp.window.Canvas()
	if canvas == nil {
		return
	}
	items := make([]*fyne.MenuItem, 0, len(suggestions)+len(kws))
	for _, s := range suggestions {
		s := s
		items = append(items, fyne.NewMenuItem(s, func() {
			qp.insertCompletion(start, wordLen, s)
		}))
	}
	if len(kws) > 0 && len(items) > 0 {
		items = append(items, fyne.NewMenuItemSeparator())
	}
	for _, k := range kws {
		k := k
		items = append(items, fyne.NewMenuItem(k.label, func() {
			// Replace the (partial) keyword with the whole saved query.
			qp.insertCompletion(start, wordLen, strings.TrimRight(k.sql, " \t\n")+"\n")
		}))
	}
	pos := fyne.NewPos(12, 12)
	if qp.editor != nil {
		pos = fyne.CurrentApp().Driver().AbsolutePositionForObject(qp.editor).AddXY(12, 40)
	}
	widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), canvas, pos)
}

// bindEditorShortcuts registers the editor's shortcuts:
//   - Ctrl+Space: completion menu
//   - Ctrl+Enter / Ctrl+Return: run the current statement
//
// Fyne delivers modified keys to the focused widget's TypedShortcut and stops
// there; when the editor (a Shortcutable Entry) has focus the canvas-level
// shortcuts never run. The keys are therefore registered on the Entry's own
// shortcut handler, with the canvas registration kept as a fallback for when
// focus is elsewhere. Safe to call repeatedly.
func (qp *queryPane) bindEditorShortcuts() {
	if qp.shortcutBound || qp.window == nil {
		return
	}
	canvas := qp.window.Canvas()
	if canvas == nil {
		return
	}
	space := &desktop.CustomShortcut{KeyName: fyne.KeySpace, Modifier: fyne.KeyModifierControl}
	canvas.AddShortcut(space, func(fyne.Shortcut) {
		if qp.editorFocused() {
			qp.complete()
		}
	})
	runKeys := []fyne.KeyName{fyne.KeyReturn}
	if fyne.KeyEnter != fyne.KeyReturn {
		runKeys = append(runKeys, fyne.KeyEnter) // keypad Enter (distinct name)
	}
	for _, key := range runKeys {
		sc := &desktop.CustomShortcut{KeyName: key, Modifier: fyne.KeyModifierControl}
		canvas.AddShortcut(sc, func(fyne.Shortcut) {
			if qp.editorFocused() {
				qp.run()
			}
		})
		// Editor focus: register on the Entry itself so the key is not
		// swallowed before the canvas sees it.
		bindEntryShortcut(qp.editor, sc, func(fyne.Shortcut) { qp.run() })
	}
	bindEntryShortcut(qp.editor, space, func(fyne.Shortcut) { qp.complete() })
	qp.shortcutBound = true
}

// bindEntryShortcut adds sc to an Entry's internal shortcut handler. Entry has
// no public registration API (Fyne v2.8, pinned in go.mod); the unexported
// field is reached through reflection so a future rename fails softly (returns
// false) instead of panicking.
func bindEntryShortcut(e *widget.Entry, sc fyne.Shortcut, fn func(fyne.Shortcut)) (ok bool) {
	if e == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	v := reflect.ValueOf(e).Elem().FieldByName("shortcut")
	if !v.IsValid() || !v.CanAddr() {
		return false
	}
	(*fyne.ShortcutHandler)(unsafe.Pointer(v.UnsafeAddr())).AddShortcut(sc, fn)
	return true
}

// ---- as-you-type completion -------------------------------------------------

// autoCompleteDelay is the typing pause before the inline suggestion popup
// refreshes (long enough not to flash on every keystroke, short enough to feel
// immediate).
const autoCompleteDelay = 120 * time.Millisecond

// autoCompleteLimit caps the inline popup so it stays a glanceable strip.
const autoCompleteLimit = 10

// splitDottedWord splits "alias.col" into the segment to match ("col") and the
// rune offset of that segment inside the word (so only the segment is
// replaced when a suggestion is accepted).
func splitDottedWord(word string) (string, int) {
	if i := strings.LastIndexByte(word, '.'); i >= 0 {
		return word[i+1:], len([]rune(word[:i+1]))
	}
	return word, 0
}

// autoPrefixOK reports whether seg is a plausible identifier prefix worth
// suggesting for: it must start with a letter/underscore (not a number or
// operator) and contain only identifier runes.
func autoPrefixOK(seg string) bool {
	if seg == "" || len([]rune(seg)) > 64 {
		return false
	}
	for i, r := range seg {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r == '.' || !isWordRune(r) {
			return false
		}
	}
	return true
}

// scheduleAutoComplete debounces the inline completion popup after an edit.
// autoGen invalidates stale timers and background lookups when the user keeps
// typing.
func (qp *queryPane) scheduleAutoComplete() {
	if qp.autoSuppress || qp.editor == nil {
		return
	}
	qp.autoGen++
	gen := qp.autoGen
	time.AfterFunc(autoCompleteDelay, func() {
		safeDo(func() {
			if gen != qp.autoGen {
				return
			}
			qp.autoComplete()
		})
	})
}

// autoComplete refreshes the inline popup for the identifier under the cursor.
// It is a no-op unless the editor is focused and the word is a plausible
// identifier prefix outside quoted strings.
func (qp *queryPane) autoComplete() {
	if qp.editor == nil || qp.window == nil || qp.window.Canvas() == nil {
		qp.hideAuto()
		return
	}
	if !qp.editorFocused() {
		qp.hideAuto()
		return
	}
	text := qp.editor.Text
	start, word := qp.currentWord()
	seg, dot := splitDottedWord(word)
	if !autoPrefixOK(seg) {
		qp.hideAuto()
		return
	}
	r := []rune(text)
	if start+dot < len(r) && inQuotes(text, len(string(r[:start+dot]))) {
		qp.hideAuto()
		return
	}
	gen := qp.autoGen
	tables := qp.tableCandidates()
	refs := referencedTables(text, tables)
	safeGo("query.autocomplete", func() {
		cols := qp.columnCandidates(refs)
		words := buildWords(tables, cols)
		sug := matchWords(words, seg, autoCompleteLimit)
		safeDo(func() {
			if gen != qp.autoGen {
				return // a newer keystroke superseded this lookup
			}
			if len(sug) == 0 {
				qp.hideAuto()
				return
			}
			qp.showAutoSuggestions(start+dot, len([]rune(seg)), sug)
		})
	})
}

// showAutoSuggestions paints (or updates) the inline popup next to the editor.
func (qp *queryPane) showAutoSuggestions(start, wordLen int, sug []string) {
	c := qp.window.Canvas()
	if c == nil {
		return
	}
	if qp.autoPop == nil || qp.autoList == nil {
		qp.autoList = container.NewVBox()
		card := newRoundedPanel(qp.autoList,
			func() color.Color { return pal().groupBG },
			func() color.Color { return pal().groupBorder }, 8, 4)
		qp.autoProxy = newEditorFocusProxy(card, qp.editor)
		qp.autoProxy.onDismiss = func() { qp.hideAuto() }
		qp.autoProxy.onKey = qp.handleAutoKey
		qp.autoPop = widget.NewPopUp(qp.autoProxy, c)
	}
	qp.autoStart, qp.autoWordLen, qp.autoWords = start, wordLen, sug
	qp.autoSel = -1 // nothing highlighted until the user presses Up/Down
	qp.autoRows = qp.autoRows[:0]
	qp.autoList.Objects = qp.autoList.Objects[:0]
	for i, s := range sug {
		i, s := i, s
		row := newSuggestionRow(s, func() { qp.acceptAuto(i) })
		qp.autoRows = append(qp.autoRows, row)
		qp.autoList.Objects = append(qp.autoList.Objects, row)
	}
	qp.autoList.Refresh()
	// PopUp sizes itself to the content's MinSize; ShowAtPosition places it in
	// canvas coordinates (same anchor the Ctrl+Space menu uses).
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(qp.editor).AddXY(12, 40)
	qp.autoPop.Resize(qp.autoPop.MinSize())
	qp.autoPop.ShowAtPosition(pos)
	qp.autoPop.Resize(qp.autoPop.MinSize())
	qp.autoPop.Refresh()
	// Overlays own an empty focus manager, so the editor would stop receiving
	// keystrokes while the popup is visible. Hand focus to the proxy, which
	// forwards everything straight back to the editor.
	c.Focus(fyne.Focusable(qp.autoProxy))
}

// handleAutoKey gives the inline suggestion list the keyboard: Up/Down move
// the highlight, Enter/Tab accept the highlighted entry. It reports whether
// the key was consumed; everything else falls through to the editor so typing
// keeps working with the popup open.
func (qp *queryPane) handleAutoKey(e *fyne.KeyEvent) bool {
	if len(qp.autoWords) == 0 {
		return false
	}
	switch e.Name {
	case fyne.KeyDown, fyne.KeyUp:
		n := len(qp.autoWords)
		switch {
		case qp.autoSel < 0:
			if e.Name == fyne.KeyDown {
				qp.autoSel = 0
			} else {
				qp.autoSel = n - 1
			}
		case e.Name == fyne.KeyDown && qp.autoSel < n-1:
			qp.autoSel++
		case e.Name == fyne.KeyUp && qp.autoSel > 0:
			qp.autoSel--
		}
		qp.highlightAuto()
		return true
	case fyne.KeyReturn, fyne.KeyEnter, fyne.KeyTab:
		if qp.autoSel >= 0 && qp.autoSel < len(qp.autoWords) {
			qp.acceptAuto(qp.autoSel)
			return true
		}
	}
	return false
}

// highlightAuto paints the keyboard selection in the suggestion list.
func (qp *queryPane) highlightAuto() {
	for i, row := range qp.autoRows {
		row.setSelected(i == qp.autoSel)
	}
}

// acceptAuto replaces the current word with suggestion i and closes the popup.
func (qp *queryPane) acceptAuto(i int) {
	if i < 0 || i >= len(qp.autoWords) {
		return
	}
	ins := qp.autoWords[i]
	start, n := qp.autoStart, qp.autoWordLen
	qp.autoGen++ // cancel pending refreshes
	qp.hideAuto()
	qp.autoSuppress = true
	qp.insertCompletion(start, n, ins)
	qp.autoSuppress = false
	qp.refreshPos()
}

// hideAuto dismisses the inline popup (no-op when nothing is shown).
func (qp *queryPane) hideAuto() {
	if qp.autoPop != nil {
		qp.autoPop.Hide()
	}
	qp.autoWords = nil
	qp.autoRows = nil
	qp.autoSel = -1
	qp.autoStart, qp.autoWordLen = 0, 0
}

// editorFocused reports whether the SQL editor owns keyboard input. While the
// inline suggestion popup is open the overlay's focus manager holds an
// editorFocusProxy, which counts as the editor being focused.
func (qp *queryPane) editorFocused() bool {
	if qp.editor == nil || qp.window == nil || qp.window.Canvas() == nil {
		return false
	}
	f := qp.window.Canvas().Focused()
	if f == fyne.Focusable(qp.editor) {
		return true
	}
	if p, ok := f.(*editorFocusProxy); ok {
		return p.target == fyne.Focusable(qp.editor)
	}
	return false
}

// editorFocusProxy wraps the suggestion card and forwards all keyboard input
// to the editor. Fyne gives every overlay its own focus manager, and an
// overlay without a focused child reports no focus at all — keyboard events
// would then go nowhere until the popup is dismissed. Focusing this proxy
// keeps typing, shortcuts and IME input working while suggestions are shown.
type editorFocusProxy struct {
	widget.BaseWidget
	content   fyne.CanvasObject
	target    fyne.Focusable
	onDismiss func()
	onKey     func(*fyne.KeyEvent) bool // optional: consumes keys the popup owns
}

var (
	_ fyne.Focusable    = (*editorFocusProxy)(nil)
	_ fyne.Shortcutable = (*editorFocusProxy)(nil)
	_ desktop.Keyable   = (*editorFocusProxy)(nil)
)

func newEditorFocusProxy(content fyne.CanvasObject, target fyne.Focusable) *editorFocusProxy {
	p := &editorFocusProxy{content: content, target: target}
	p.ExtendBaseWidget(p)
	return p
}

func (p *editorFocusProxy) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(p.content)
}

func (p *editorFocusProxy) FocusGained() {}
func (p *editorFocusProxy) FocusLost()   {}

func (p *editorFocusProxy) TypedKey(e *fyne.KeyEvent) {
	if e.Name == fyne.KeyEscape && p.onDismiss != nil {
		p.onDismiss()
		return
	}
	if p.onKey != nil && p.onKey(e) {
		return
	}
	p.target.TypedKey(e)
}

func (p *editorFocusProxy) TypedRune(r rune) { p.target.TypedRune(r) }

func (p *editorFocusProxy) TypedShortcut(s fyne.Shortcut) {
	if sc, ok := p.target.(fyne.Shortcutable); ok {
		sc.TypedShortcut(s)
	}
}

func (p *editorFocusProxy) KeyDown(e *fyne.KeyEvent) {
	if k, ok := p.target.(desktop.Keyable); ok {
		k.KeyDown(e)
	}
}

func (p *editorFocusProxy) KeyUp(e *fyne.KeyEvent) {
	if k, ok := p.target.(desktop.Keyable); ok {
		k.KeyUp(e)
	}
}

// ---- suggestion row widget --------------------------------------------------

// suggestionRowHeight is the fixed row height of the inline popup list.
const suggestionRowHeight = float32(24)

// suggestionRow is one clickable line of the inline completion popup:
// left-aligned text with a hover highlight.
type suggestionRow struct {
	widget.BaseWidget
	label    string
	onTap    func()
	hover    bool
	selected bool // keyboard highlight (Up/Down)
}

func newSuggestionRow(label string, onTap func()) *suggestionRow {
	r := &suggestionRow{label: label, onTap: onTap}
	r.ExtendBaseWidget(r)
	return r
}

func (r *suggestionRow) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap()
	}
}

func (r *suggestionRow) Cursor() desktop.Cursor { return desktop.PointerCursor }

// setSelected marks the row as the current keyboard choice.
func (r *suggestionRow) setSelected(on bool) {
	if r.selected != on {
		r.selected = on
		r.Refresh()
	}
}

func (r *suggestionRow) MouseIn(*desktop.MouseEvent)    { r.hover = true; r.Refresh() }
func (r *suggestionRow) MouseOut()                      { r.hover = false; r.Refresh() }
func (r *suggestionRow) MouseMoved(*desktop.MouseEvent) {}

func (r *suggestionRow) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(colorTransparent)
	bg.CornerRadius = 4
	text := canvas.NewText(r.label, pal().foreground)
	text.TextSize = 12
	return &suggestionRowRenderer{r: r, bg: bg, text: text}
}

type suggestionRowRenderer struct {
	r    *suggestionRow
	bg   *canvas.Rectangle
	text *canvas.Text
}

func (rd *suggestionRowRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{rd.bg, rd.text}
}

func (rd *suggestionRowRenderer) MinSize() fyne.Size {
	w := rd.text.MinSize().Width + 24
	if w < 160 {
		w = 160
	}
	return fyne.NewSize(w, suggestionRowHeight)
}

func (rd *suggestionRowRenderer) Layout(size fyne.Size) {
	rd.bg.Resize(size)
	t := rd.text.MinSize()
	rd.text.Move(fyne.NewPos(10, (size.Height-t.Height)/2))
}

func (rd *suggestionRowRenderer) Destroy() {}

func (rd *suggestionRowRenderer) Refresh() {
	p := pal()
	if rd.r.selected {
		rd.bg.FillColor = p.treeSel // same accent used for sidebar selection
		rd.text.Color = colorWhite
	} else {
		if rd.r.hover {
			rd.bg.FillColor = p.hover
		} else {
			rd.bg.FillColor = colorTransparent
		}
		rd.text.Color = p.foreground
	}
	rd.text.Text = rd.r.label
	rd.bg.Refresh()
	rd.text.Refresh()
}
