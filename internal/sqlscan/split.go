package sqlscan

// The two statement splitters, moved verbatim from their call sites
// (wave-49 convergence): StatementRanges serves the editor's Run All /
// Run Current (byte ranges, one statement per semicolon at quote/comment
// depth 0); SplitDelimited serves the backup restore path (strings, plus
// DELIMITER directives and BEGIN…END-aware body keeping). Their SEMANTICS
// stay distinct this wave — merging them changes Run All's behavior for
// routine bodies and needs its own reviewed change — but they now share one
// home, one test surface and one doc.

import "strings"

// StmtRange is the [Start,End) byte span of one top-level statement.
type StmtRange struct{ Start, End int }

// StatementRanges scans a script honouring quoted strings, -- line comments
// and /* block */ comments, returning the span of every top-level statement
// (semicolon-terminated, plus the trailing unterminated span if non-empty —
// callers decide whether to run it).
func StatementRanges(script string) []StmtRange {
	var out []StmtRange
	start := 0
	var q byte // active quote char (0 = none)
	inLine, inBlock := false, false
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
			}
		case inBlock:
			if c == '*' && i+1 < len(script) && script[i+1] == '/' {
				inBlock = false
				i++
			}
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '\'' || c == '"':
			q = c
		case c == '-' && i+1 < len(script) && script[i+1] == '-':
			inLine = true
			i++
		case c == '/' && i+1 < len(script) && script[i+1] == '*':
			inBlock = true
			i++
		case c == ';':
			out = append(out, StmtRange{start, i + 1})
			start = i + 1
		}
	}
	out = append(out, StmtRange{start, len(script)})
	return out
}

// SplitDelimited splits a SQL script into statements, honoring DELIMITER
// directives (as mysqldump emits around stored routines/triggers).
// Semicolons inside BEGIN…END blocks are kept intact until the active
// delimiter appears, while string literals, quoted identifiers, -- line
// comments and /* block */ comments are respected so a delimiter inside them
// does not split. The MySQL executable form /*! ... */ is kept as statement
// text (mysqldump's version-guarded directives live in it).
func SplitDelimited(script string) []string {
	var out []string
	var sb strings.Builder
	delim := ";"
	var quote byte  // 0, '\'', '"' or '`'
	atStart := true // sb holds only whitespace so far (statement boundary)

	flush := func() {
		if s := strings.TrimSpace(sb.String()); s != "" {
			out = append(out, s)
		}
		sb.Reset()
		atStart = true
	}

	i, n := 0, len(script)
	for i < n {
		// At a statement start look for a DELIMITER directive before anything
		// else, so routine bodies that use the custom delimiter are kept whole.
		if atStart {
			if d, consumed, ok := readDelimiterDirective(script[i:]); ok {
				flush()
				delim = d
				i += consumed
				continue
			}
		}

		c := script[i]
		if quote != 0 {
			sb.WriteByte(c)
			if c == '\\' && quote != '`' && i+1 < n {
				sb.WriteByte(script[i+1])
				i += 2
				continue
			}
			if c == quote {
				quote = 0
			}
			i++
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			sb.WriteByte(c)
			atStart = false
		case '-':
			if i+1 < n && script[i+1] == '-' {
				for i < n && script[i] != '\n' {
					i++
				}
				sb.WriteByte('\n')
				continue
			}
			sb.WriteByte(c)
			atStart = false
		case '/':
			if i+1 < n && script[i+1] == '*' {
				end := -1
				if j := strings.Index(script[i+2:], "*/"); j >= 0 {
					end = i + 2 + j + 2
				}
				if i+2 < n && script[i+2] == '!' {
					// MySQL executable conditional comment (/*! ... */): to the
					// server this is statement text (mysqldump's version-guarded
					// directives live in it), so keep it verbatim — including
					// its closing */ so it is not misread as a comment opener.
					if end < 0 {
						sb.WriteString(script[i:])
						i = n
					} else {
						sb.WriteString(script[i:end])
						i = end
					}
					atStart = false
					continue
				}
				// Plain block comment: skip through the closing */ (as
				// whitespace), so a semicolon or DELIMITER token inside it
				// cannot split.
				if end < 0 {
					i = n // unterminated: drop the rest of the script
				} else {
					i = end
				}
				sb.WriteByte(' ')
				continue
			}
			if strings.HasPrefix(script[i:], delim) {
				flush()
				i += len(delim)
				continue
			}
			sb.WriteByte(c)
			atStart = false
		default:
			if strings.HasPrefix(script[i:], delim) {
				flush()
				i += len(delim)
				continue
			}
			sb.WriteByte(c)
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				atStart = false
			}
		}
		i++
	}
	flush()
	return out
}

// readDelimiterDirective parses a DELIMITER directive at the start of s and
// returns the delimiter token plus the number of bytes consumed (through the
// end of that directive line).
func readDelimiterDirective(s string) (string, int, bool) {
	if len(s) < 9 || !strings.EqualFold(s[:9], "DELIMITER") {
		return "", 0, false
	}
	after := 9
	if after >= len(s) || (s[after] != ' ' && s[after] != '\t' && s[after] != '\r' && s[after] != '\n') {
		return "", 0, false
	}
	for after < len(s) && (s[after] == ' ' || s[after] == '\t' || s[after] == '\r' || s[after] == '\n') {
		after++
	}
	start := after
	for after < len(s) && s[after] != ' ' && s[after] != '\t' && s[after] != '\r' && s[after] != '\n' {
		after++
	}
	if start == after {
		return "", 0, false
	}
	delim := s[start:after]
	for after < len(s) && s[after] != '\n' {
		after++
	}
	if after < len(s) {
		after++ // consume the newline
	}
	return delim, after, true
}
