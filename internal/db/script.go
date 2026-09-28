package db

import (
	"strings"
)

// SplitStatements breaks a script into statements at semicolons that sit
// outside string literals, quoted identifiers and comments, so a pasted
// script runs statement by statement. Limitations, on purpose: BEGIN...END
// blocks (stored procedures, triggers) hide semicolons from this splitter and
// must be executed one statement at a time.
func SplitStatements(script string) []string {
	var out []string
	var sb strings.Builder
	flush := func() {
		s := strings.TrimSpace(sb.String())
		sb.Reset()
		if s == "" {
			return
		}
		// A chunk that is only comments has nothing to run; sending it would
		// just make the server echo an empty statement back.
		if strings.TrimSpace(sqlCommentRe.ReplaceAllString(s, " ")) == "" {
			return
		}
		out = append(out, s)
	}

	runes := []rune(script)
	n := len(runes)
	for i := 0; i < n; i++ {
		switch r := runes[i]; r {
		case '\'':
			sb.WriteRune(r)
			i = scanQuoted(runes, i, r, &sb)
		case '"', '`':
			sb.WriteRune(r)
			i = scanQuoted(runes, i, r, &sb)
		case '-':
			if i+1 < n && runes[i+1] == '-' {
				sb.WriteString("--")
				i++
				for i+1 < n && runes[i] != '\n' {
					i++
					sb.WriteRune(runes[i])
				}
				continue
			}
			sb.WriteRune(r)
		case '/':
			if i+1 < n && runes[i+1] == '*' {
				sb.WriteString("/*")
				i++
				for i+1 < n {
					i++
					sb.WriteRune(runes[i])
					if runes[i] == '*' && i+1 < n && runes[i+1] == '/' {
						i++
						sb.WriteRune(runes[i])
						break
					}
				}
				continue
			}
			sb.WriteRune(r)
		case ';':
			flush()
		default:
			sb.WriteRune(r)
		}
	}
	flush()
	return out
}

// scanQuoted copies a quoted region from the opening quote at i to its close,
// honoring doubled quotes, and returns the index of the closing quote.
func scanQuoted(runes []rune, i int, quote rune, sb *strings.Builder) int {
	n := len(runes)
	for i+1 < n {
		i++
		sb.WriteRune(runes[i])
		if runes[i] == quote {
			if i+1 < n && runes[i+1] == quote { // doubled quote stays inside
				i++
				sb.WriteRune(runes[i])
				continue
			}
			return i
		}
	}
	return i
}
