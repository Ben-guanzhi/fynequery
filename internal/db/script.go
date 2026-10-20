package db

import (
	"strings"
	"unicode"
)

// StmtSpan is one statement together with where it sits in the script: Start
// is the offset of its first character and End the offset one past its last
// one (the terminating semicolon included, so a caret resting on that
// semicolon still counts as being inside the statement). Text is the
// executable statement itself - leading/trailing space and the semicolon are
// not part of it.
//
// Byte offsets, not rune offsets: everything downstream (a caret position
// turned into an offset) already counts bytes.
type StmtSpan struct {
	Start, End int
	Text       string
}

// SplitStatementsSpans breaks a script into statements at semicolons that sit
// outside string literals, quoted identifiers and comments, keeping each
// statement's position in the original text. SplitStatements is this function
// with the positions thrown away.
func SplitStatementsSpans(script string) []StmtSpan {
	var out []StmtSpan
	// start is where the current statement's raw slice begins; it is moved to
	// just past each semicolon, and the leading whitespace of the slice is
	// trimmed away at flush time.
	start := 0
	// contentEnd is exclusive and stops before the semicolon; spanEnd is one
	// past the semicolon, which is what End is meant to say. For the final
	// statement (no semicolon) they are the same number.
	flush := func(contentEnd, spanEnd int) {
		if contentEnd < start {
			return
		}
		raw := script[start:contentEnd]
		lo := len(raw) - len(strings.TrimLeftFunc(raw, unicode.IsSpace))
		hi := len(strings.TrimRightFunc(raw, unicode.IsSpace))
		if hi <= lo {
			return
		}
		text := raw[lo:hi]
		// A chunk that is only comments has nothing to run; sending it would
		// just make the server echo an empty statement back.
		if strings.TrimSpace(sqlCommentRe.ReplaceAllString(text, " ")) == "" {
			return
		}
		out = append(out, StmtSpan{Start: start + lo, End: spanEnd, Text: text})
	}

	n := len(script)
	for i := 0; i < n; {
		switch script[i] {
		case '\'':
			i = skipQuoted(script, i, '\'')
		case '"', '`':
			i = skipQuoted(script, i, script[i])
		case '-':
			if i+1 < n && script[i+1] == '-' {
				i += 2
				for i < n && script[i] != '\n' {
					i++
				}
				if i < n {
					i++ // the newline ends the comment and is part of it
				}
				continue
			}
			i++
		case '/':
			if i+1 < n && script[i+1] == '*' {
				i += 2
				for i+1 < n {
					if script[i] == '*' && script[i+1] == '/' {
						i += 2
						break
					}
					i++
				}
				continue
			}
			i++
		case ';':
			flush(i, i+1)
			start = i + 1
			i++
		default:
			i++
		}
	}
	flush(n, n)
	return out
}

// skipQuoted steps over a quoted region beginning at the opening quote at i
// (string literal, or a " or ` quoted identifier), honouring doubled quotes,
// and returns the index one past the closing quote. An unterminated region
// swallows the rest of the script, which is how the statement splitters have
// always read it - see the limitation in SplitStatements.
//
// Quotes are ASCII, so walking bytes is safe inside UTF-8 text: every
// continuation byte is >= 0x80 and can never compare equal to one of them.
func skipQuoted(s string, i int, quote byte) int {
	n := len(s)
	for i+1 < n {
		i++
		if s[i] == quote {
			if i+1 < n && s[i+1] == quote { // doubled quote stays inside
				i++
				continue
			}
			return i + 1
		}
	}
	return n
}

// StmtAt answers which statement a caret offset belongs to.
//
// Inside a statement's span is inside the statement, whatever that position
// happens to hold: a semicolon in a string literal, a commented-out clause or
// a newline all sit between Start and End, so no second tokenizer is needed
// to answer this. An offset in the gap between two statements - the blank run
// after a semicolon, a comment-only block - has no statement of its own, and
// the rule there is the one a database IDE follows: the caret still reads as
// belonging to the statement above it, so pressing Enter there re-runs what
// was just run. Only when there is nothing above does it fall forward to the
// next statement, and only a script of nothing but comments has no answer.
func StmtAt(script string, off int) (StmtSpan, bool) {
	spans := SplitStatementsSpans(script)
	if len(spans) == 0 {
		return StmtSpan{}, false
	}
	for _, s := range spans {
		if off >= s.Start && off <= s.End {
			return s, true
		}
	}
	var prev StmtSpan
	havePrev := false
	for _, s := range spans {
		if s.End < off {
			prev, havePrev = s, true
			continue
		}
		if s.Start > off {
			if havePrev {
				return prev, true
			}
			return s, true
		}
	}
	if havePrev {
		return prev, true
	}
	return spans[len(spans)-1], true
}

// StmtsInRange returns every statement the selection range touches, in script
// order. A partly covered statement counts as covered: running half a
// statement is not a thing this offers, so the caller gets whole statements
// and can say so.
func StmtsInRange(script string, start, end int) []StmtSpan {
	if end < start {
		start, end = end, start
	}
	var out []StmtSpan
	for _, s := range SplitStatementsSpans(script) {
		if s.End < start || s.Start > end {
			continue
		}
		out = append(out, s)
	}
	return out
}

// SplitStatements breaks a script into statements at semicolons that sit
// outside string literals, quoted identifiers and comments, so a pasted
// script runs statement by statement. Limitations, on purpose: BEGIN...END
// blocks (stored procedures, triggers) hide semicolons from this splitter and
// must be executed one statement at a time.
func SplitStatements(script string) []string {
	spans := SplitStatementsSpans(script)
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, s.Text)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
