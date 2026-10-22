package main

import (
	"strings"
	"unicode"
)

// sqlFmtKeywords is the set of SQL keywords that get uppercased during formatting.
// Identifiers and other words keep their original case.
var sqlFmtKeywords = map[string]bool{
	"select": true, "from": true, "where": true, "and": true, "or": true,
	"not": true, "null": true, "as": true, "on": true, "in": true, "is": true,
	"like": true, "between": true, "exists": true, "group": true, "by": true,
	"order": true, "having": true, "limit": true, "offset": true, "union": true,
	"all": true, "distinct": true, "join": true, "inner": true, "left": true,
	"right": true, "outer": true, "full": true, "cross": true, "using": true,
	"insert": true, "into": true, "values": true, "update": true, "set": true,
	"delete": true, "create": true, "table": true, "view": true, "index": true,
	"alter": true, "drop": true, "truncate": true, "replace": true, "with": true,
	"case": true, "when": true, "then": true, "else": true, "end": true,
	"asc": true, "desc": true, "default": true, "primary": true, "key": true,
	"foreign": true, "references": true, "unique": true, "constraint": true,
	"if": true, "begin": true, "commit": true, "rollback": true,
	"transaction": true, "cast": true, "interval": true, "true": true, "false": true,
	"over": true, "partition": true, "collate": true, "nulls": true,
}

// sqlBreakBefore lists keywords that start a new line when they begin a clause.
var sqlBreakBefore = map[string]bool{
	"from": true, "where": true, "group": true, "order": true, "having": true,
	"limit": true, "offset": true, "union": true, "values": true, "set": true,
	"on": true, "and": true, "or": true,
	"inner": true, "left": true, "right": true, "outer": true, "full": true, "cross": true,
}

// joinQualifiers precede "join"; we must not break again before the "join".
var joinQualifiers = map[string]bool{
	"inner": true, "left": true, "right": true, "outer": true, "full": true, "cross": true,
}

type sqlTokenKind int

const (
	tokSpace sqlTokenKind = iota
	tokWord
	tokString
	tokPunct
	tokLineComment
	tokBlockComment
)

type sqlToken struct {
	kind sqlTokenKind
	text string
}

// tokenizeSQL splits SQL into tokens, keeping string literals and comments
// verbatim so their contents are never uppercased or re-spaced.
func tokenizeSQL(s string) []sqlToken {
	var toks []sqlToken
	rs := []rune(s)
	i := 0
	for i < len(rs) {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			i++
			toks = append(toks, sqlToken{tokSpace, " "})
			for i < len(rs) && unicode.IsSpace(rs[i]) {
				i++
			}
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-':
			j := i + 2
			for j < len(rs) && rs[j] != '\n' {
				j++
			}
			toks = append(toks, sqlToken{tokLineComment, string(rs[i:j])})
			i = j
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*':
			j := i + 2
			for j+1 < len(rs) && !(rs[j] == '*' && rs[j+1] == '/') {
				j++
			}
			if j+1 < len(rs) {
				j += 2
			}
			toks = append(toks, sqlToken{tokBlockComment, string(rs[i:j])})
			i = j
		case c == '\'' || c == '"' || c == '`':
			quote := c
			j := i + 1
			for j < len(rs) {
				if rs[j] == quote {
					if j+1 < len(rs) && rs[j+1] == quote {
						j += 2 // escaped quote
						continue
					}
					j++
					break
				}
				j++
			}
			toks = append(toks, sqlToken{tokString, string(rs[i:j])})
			i = j
		case unicode.IsLetter(c) || c == '_' || c == '$':
			j := i + 1
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_' || rs[j] == '$') {
				j++
			}
			toks = append(toks, sqlToken{tokWord, string(rs[i:j])})
			i = j
		case unicode.IsDigit(c):
			j := i + 1
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.') {
				j++
			}
			toks = append(toks, sqlToken{tokWord, string(rs[i:j])})
			i = j
		default:
			j := i + 1
			// multi-char operators: <= >= <> != || ; keep together
			if strings.ContainsRune("<>=!|", c) {
				for j < len(rs) && strings.ContainsRune("<>=!|", rs[j]) {
					j++
				}
			}
			toks = append(toks, sqlToken{tokPunct, string(rs[i:j])})
			i = j
		}
	}
	return toks
}

// punctCategory classifies a punctuation token for spacing decisions.
func punctCategory(t string) string {
	switch t {
	case "(":
		return "open"
	case ")":
		return "close"
	case ",":
		return "comma"
	case ".":
		return "dot"
	case ";":
		return "semi"
	case "*":
		return "atom" // treat like an identifier for spacing
	default:
		return "op"
	}
}

// formatSQL pretty-prints SQL: uppercases keywords, puts major clauses on
// their own line, indents AND/OR continuations, and leaves string literals and
// comments untouched. It is a best-effort formatter, not a full parser.
func formatSQL(in string) string {
	toks := tokenizeSQL(in)
	var cur strings.Builder
	lines := []string{}
	atLineStart := true
	prevCat := ""
	prevKwLower := ""
	depth := 0

	flush := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		atLineStart = true
		prevCat = ""
	}

	// emit writes text of the given spacing category, handling line breaks
	// (semi) and leading/trailing spaces.
	emit := func(text, cat string) {
		if cat == "semi" {
			if !atLineStart {
				cur.WriteString(text)
			}
			flush()
			return
		}
		if !atLineStart {
			needSpace := true
			if prevCat == "open" || prevCat == "dot" || prevCat == "comma" {
				needSpace = false
			}
			if cat == "comma" || cat == "close" || cat == "dot" {
				needSpace = false
			}
			if needSpace {
				cur.WriteString(" ")
			}
		}
		cur.WriteString(text)
		if cat == "comma" {
			cur.WriteString(" ")
		}
		atLineStart = false
		prevCat = cat
	}

	for _, t := range toks {
		switch t.kind {
		case tokSpace:
			continue
		case tokLineComment:
			emit(t.text, "atom")
			flush()
		case tokBlockComment:
			emit(t.text, "atom")
			flush()
		case tokString:
			emit(t.text, "atom")
		case tokPunct:
			cat := punctCategory(t.text)
			if cat == "open" {
				depth++
			} else if cat == "close" {
				if depth > 0 {
					depth--
				}
			}
			emit(t.text, cat)
		case tokWord:
			lower := strings.ToLower(t.text)
			if sqlFmtKeywords[lower] {
				kw := strings.ToUpper(t.text)
				force := false
				indent := 0
				if sqlBreakBefore[lower] {
					switch lower {
					case "and", "or":
						if depth == 0 {
							force = true
							indent = 2
						}
					case "on":
						if prevKwLower != "join" {
							force = true
						}
					case "join":
						if prevKwLower == "" || !joinQualifiers[prevKwLower] {
							force = true
						}
					default:
						force = true
					}
				}
				if force {
					flush()
					cur.WriteString(strings.Repeat(" ", indent))
					cur.WriteString(kw)
					atLineStart = false
					prevCat = "atom"
				} else {
					emit(kw, "atom")
				}
				prevKwLower = lower
			} else {
				emit(t.text, "atom")
				prevKwLower = ""
			}
		}
	}
	if !atLineStart {
		flush()
	}
	return strings.Join(lines, "\n")
}
