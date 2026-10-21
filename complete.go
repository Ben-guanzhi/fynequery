package main

// SQL editor autocomplete. Fyne's widget.Entry has no built-in completion, so
// we implement a lightweight one: Ctrl+Space (or the Complete button) pops a
// menu of SQL keywords, functions and identifiers (tables/columns) filtered by
// the word under the cursor; picking one inserts it in place of that word.

import (
	"sort"
	"strings"
	"unicode"

	"fyne.io/fyne/v2"
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

// bindCompletionShortcut registers Ctrl+Space on the window canvas so the
// completion menu can be triggered from the editor. Safe to call repeatedly.
func (qp *queryPane) bindCompletionShortcut() {
	if qp.shortcutBound || qp.window == nil {
		return
	}
	canvas := qp.window.Canvas()
	if canvas == nil {
		return
	}
	canvas.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeySpace, Modifier: fyne.KeyModifierControl},
		func(fyne.Shortcut) {
			if canvas.Focused() == fyne.Focusable(qp.editor) {
				qp.complete()
			}
		})
	qp.shortcutBound = true
}
