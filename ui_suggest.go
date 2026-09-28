package main

// The completion half of the SQL editor: the suggestEntry control (word
// detection, candidate filtering, key interception), the keyword seed of the
// candidate pool, and the shared monospace text metrics. Tab management, the
// gutter, and the candidate bar live in ui_editor.go.

import (
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// sqlKeywords seeds the completion pool; database and table names come from
// the live connection on top of these.
var sqlKeywords = []string{
	"SELECT", "FROM", "WHERE", "INSERT", "INTO", "VALUES", "UPDATE", "SET",
	"DELETE", "JOIN", "LEFT", "RIGHT", "INNER", "OUTER", "FULL", "ON",
	"GROUP BY", "ORDER BY", "HAVING", "LIMIT", "OFFSET", "UNION", "ALL",
	"DISTINCT", "AS", "AND", "OR", "NOT", "NULL", "IS", "IN", "BETWEEN",
	"LIKE", "EXISTS", "CASE", "WHEN", "THEN", "ELSE", "END", "CREATE",
	"TABLE", "VIEW", "INDEX", "DROP", "ALTER", "ADD", "PRIMARY KEY",
	"FOREIGN KEY", "REFERENCES", "DEFAULT", "TRUNCATE", "ASC", "DESC",
	"COUNT", "SUM", "AVG", "MIN", "MAX", "WITH", "RETURNING", "TOP",
	"SHOW", "DESCRIBE", "EXPLAIN",
}

// maxSuggestions caps the candidate bar height.
const maxSuggestions = 5

// wordSpan locates the identifier under the caret within one line.
type wordSpan struct {
	row, start, end int
	text            string
}

// suggestEntry wraps the SQL editor with completion. fyne ships no
// CompletionEntry, so key handling and word splicing live here; the candidate
// bar itself is rendered by the client below the editor. While candidates are
// visible, Up/Down move the selection, Enter accepts it and Esc dismisses.
//
// The Entry is VALUE-embedded and ExtendBaseWidget points the widget identity
// at the wrapper: with a pointer-embedded wrapper the theme/refresh plumbing
// keeps targeting the inner entry and, e.g., dark theme never reaches it.
type suggestEntry struct {
	widget.Entry
	suggest      []string
	sugSel       int
	onSugChanged func() // re-renders the candidate bar; wired by the client
	onLines      func() // re-syncs the line-number gutter; wired by the client
}

// newSuggestEntry builds a multi-line SQL editor with completion support.
func newSuggestEntry() *suggestEntry {
	s := &suggestEntry{}
	s.MultiLine = true
	s.ExtendBaseWidget(s)
	return s
}

// MinSize grows with the text's line count, so the editor fills the tab's
// scroller instead of scrolling internally; the gutter tracks it.
func (s *suggestEntry) MinSize() fyne.Size {
	editorMetrics()
	n := strings.Count(s.Text, "\n") + 1
	if n <= 1 {
		return fyne.NewSize(editorBaseW, editorBaseH)
	}
	return fyne.NewSize(editorBaseW, editorBaseH+float32(n-1)*editorLineH)
}

// editorMetrics measures the monospace text metrics once: the per-line step
// (from a two-line label delta) and the editor's one-line base size, which
// the growing MinSize and the gutter both rely on.
var (
	editorMetricsOnce sync.Once
	editorLineH       float32 // advance of one text line
	editorBaseH       float32 // editor min height at one line
	editorBaseW       float32 // editor min width
)

func editorMetrics() {
	editorMetricsOnce.Do(func() {
		l1 := widget.NewLabel("Mg")
		l1.TextStyle = fyne.TextStyle{Monospace: true}
		l2 := widget.NewLabel("Mg\nMg")
		l2.TextStyle = fyne.TextStyle{Monospace: true}
		editorLineH = l2.MinSize().Height - l1.MinSize().Height
		probe := widget.NewMultiLineEntry()
		probe.TextStyle = fyne.TextStyle{Monospace: true}
		editorBaseH = probe.MinSize().Height
		editorBaseW = probe.MinSize().Width
		if editorLineH <= 0 {
			editorLineH = fyne.MeasureText("Mg", theme.TextSize(), fyne.TextStyle{Monospace: true}).Height
		}
	})
}

// TypedKey intercepts navigation keys while the candidate bar is open and
// forwards everything else to the entry.
func (s *suggestEntry) TypedKey(k *fyne.KeyEvent) {
	if len(s.suggest) > 0 {
		switch k.Name {
		case fyne.KeyDown:
			if s.sugSel < len(s.suggest)-1 {
				s.sugSel++
				s.notifySug()
			}
			return
		case fyne.KeyUp:
			if s.sugSel > 0 {
				s.sugSel--
				s.notifySug()
			}
			return
		case fyne.KeyEnter, fyne.KeyReturn:
			s.acceptSuggestion()
			return
		case fyne.KeyEscape:
			s.setSuggestions(nil)
			return
		}
	}
	s.Entry.TypedKey(k)
}

// setTextSilently replaces the editor text without firing the completion
// pass: the change callback is detached for the duration, so a program fill
// (tree click, history, filter) never looks like typing.
func (s *suggestEntry) setTextSilently(text string) {
	if text == s.Entry.Text {
		return
	}
	cb := s.OnChanged
	s.OnChanged = nil
	s.Entry.SetText(text)
	s.OnChanged = cb
	if s.onLines != nil {
		s.onLines()
	}
}

func (s *suggestEntry) notifySug() {
	if s.onSugChanged != nil {
		s.onSugChanged()
	}
}

func (s *suggestEntry) setSuggestions(list []string) {
	s.suggest = list
	s.sugSel = 0
	s.notifySug()
}

func (s *suggestEntry) acceptSuggestion() {
	if len(s.suggest) == 0 || s.sugSel >= len(s.suggest) {
		return
	}
	choice := s.suggest[s.sugSel]
	w := s.currentWord()
	if w.text == "" {
		return
	}
	cb := s.OnChanged
	s.OnChanged = nil
	s.Entry.SetText(replaceWord(s.Entry.Text, w, choice))
	s.OnChanged = cb
	s.Entry.CursorRow = w.row
	s.Entry.CursorColumn = w.start + utf8.RuneCountInString(choice)
	s.setSuggestions(nil)
	if s.onLines != nil {
		s.onLines()
	}
}

// currentWord returns the identifier under the caret, if any.
func (s *suggestEntry) currentWord() wordSpan {
	row, col := s.CursorRow, s.CursorColumn
	lines := strings.Split(s.Entry.Text, "\n")
	if row < 0 || row >= len(lines) {
		return wordSpan{}
	}
	line := []rune(lines[row])
	if col > len(line) {
		col = len(line)
	}
	start, end := wordBounds(line, col)
	return wordSpan{row: row, start: start, end: end, text: string(line[start:end])}
}

// wordBounds expands from col in both directions over identifier characters.
func wordBounds(line []rune, col int) (start, end int) {
	isWord := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
	}
	start = col
	for start > 0 && isWord(line[start-1]) {
		start--
	}
	end = col
	for end < len(line) && isWord(line[end]) {
		end++
	}
	return
}

// replaceWord splices repl over the word span in text.
func replaceWord(text string, w wordSpan, repl string) string {
	lines := strings.Split(text, "\n")
	line := []rune(lines[w.row])
	lines[w.row] = string(line[:w.start]) + repl + string(line[w.end:])
	return strings.Join(lines, "\n")
}

// matchSuggestions filters pool by case-insensitive prefix, dropping exact
// matches and duplicates, capped to keep the bar compact.
func matchSuggestions(pool []string, word string) []string {
	lw := strings.ToLower(word)
	seen := make(map[string]bool, len(pool))
	var out []string
	for _, p := range pool {
		if p == "" {
			continue
		}
		lp := strings.ToLower(p)
		if lp == lw || !strings.HasPrefix(lp, lw) || seen[lp] {
			continue
		}
		seen[lp] = true
		out = append(out, p)
		if len(out) >= maxSuggestions {
			break
		}
	}
	sort.Strings(out)
	return out
}
