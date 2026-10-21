package main

// Query parameters + result naming, modeled on Beekeeper Studio's
// ":variable" prompt and TablePlus' "-- name:" result labels.
//
// A statement containing :name / $name / ${name} / {{name}} / %name% / ?
// placeholders (outside quoted strings) prompts the user for values before
// running; entered values are remembered (per name, in queryvars.json) so
// re-runs pre-fill the dialog.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// varFile is where remembered parameter values live (next to connections.json).
func varFile() string { return filepath.Join(configDir(), "queryvars.json") }

// loadQueryVars reads the remembered values ({} when missing/corrupt).
func loadQueryVars() map[string]string {
	raw, err := os.ReadFile(varFile())
	if err != nil {
		return map[string]string{}
	}
	m := map[string]string{}
	_ = json.Unmarshal(raw, &m)
	return m
}

// saveQueryVars persists the remembered values.
func saveQueryVars(m map[string]string) {
	_ = os.MkdirAll(configDir(), 0o755)
	if raw, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(varFile(), raw, 0o600)
	}
}

// paramRe matches the supported placeholder shapes. Longer forms are listed
// first so ${x} wins over $x, and ? is the positional form.
var paramRe = regexp.MustCompile(
	`\$\{([A-Za-z_][\w]*)\}|\{\{([A-Za-z_][\w]*)\}\}|:([A-Za-z_][\w]*)|%([A-Za-z_][\w]*)%|\$([A-Za-z_][\w]*)|\?`)

// limitRe matches a LIMIT/OFFSET keyword (standalone word) in code with
// strings/comments already blanked out.
var limitRe = regexp.MustCompile(`(?i)\b(limit|offset)\b`)

// limitBlockRe matches clauses that must stay at the very end of a SELECT
// (MySQL/PostgreSQL put LIMIT before FOR UPDATE and reject a clause appended
// after it; INTO OUTFILE/DUMPFILE likewise terminates the statement).
var limitBlockRe = regexp.MustCompile(`(?i)\b(for\s+(?:no\s+key\s+)?(?:update|share)|lock\s+in\s+share\s+mode|into\s+(?:out|dump)file)\b`)

// topRe matches the SQL Server row cap (SELECT [DISTINCT] TOP n …), which
// already limits the result — appending LIMIT would be wrong.
var topRe = regexp.MustCompile(`(?i)\bselect\s+(?:distinct\s+|all\s+)?top\b`)

// inQuotes reports whether pos falls inside a 'single' or "double" quoted
// span (placeholders there are literal text, not parameters).
func inQuotes(s string, pos int) bool {
	var q byte
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		switch c {
		case '\\':
			if q != 0 {
				escaped = true
			}
		case '\'':
			if q == '\'' {
				q = 0
			} else if q == 0 {
				q = '\''
			}
		case '"':
			if q == '"' {
				q = 0
			} else if q == 0 {
				q = '"'
			}
		}
		if i == pos {
			return q != 0
		}
	}
	return false
}

// extractParams scans a statement and returns the placeholder names in order
// of first appearance (duplicates collapse to one entry; positional ? is
// named ?1, ?2, …).
func extractParams(sqlText string) []string {
	var names []string
	seen := map[string]bool{}
	positional := 0
	for _, m := range paramRe.FindAllStringSubmatchIndex(sqlText, -1) {
		start := m[0]
		if inQuotes(sqlText, start) {
			continue
		}
		groups := m[2:]
		var name string
		for i := 0; i < len(groups); i += 2 {
			if groups[i] >= 0 {
				name = sqlText[groups[i]:groups[i+1]]
				break
			}
		}
		if name == "" {
			positional++
			name = "?" + strconv.Itoa(positional)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// substituteParams replaces every placeholder with its value (verbatim,
// TablePlus/Beekeeper behaviour — strings must be pre-quoted by the user).
// Placeholders inside quoted strings are left alone; unknown names are kept.
func substituteParams(sqlText string, vals map[string]string) string {
	var b strings.Builder
	last := 0
	positional := 0
	for _, m := range paramRe.FindAllStringSubmatchIndex(sqlText, -1) {
		start, end := m[0], m[1]
		if inQuotes(sqlText, start) {
			continue
		}
		var name string
		for i := 2; i < len(m); i += 2 {
			if m[i] >= 0 {
				name = sqlText[m[i]:m[i+1]]
				break
			}
		}
		if name == "" {
			positional++
			name = "?" + strconv.Itoa(positional)
		}
		v, ok := vals[name]
		if !ok {
			continue
		}
		b.WriteString(sqlText[last:start])
		b.WriteString(v)
		last = end
	}
	b.WriteString(sqlText[last:])
	return b.String()
}

// resultName extracts a "-- name: …" / "-- label: …" / "-- tab: …" comment
// from the statement (TablePlus uses it to label the result tab); "" when
// absent.
func resultName(sqlText string) string {
	re := regexp.MustCompile(`(?m)^\s*--\s*(?:name|label|tab)\s*:\s*(.+)$`)
	if m := re.FindStringSubmatch(sqlText); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// hasTopLevelLimit reports whether the (single) statement already carries a
// LIMIT/OFFSET clause that should not be wrapped. It scans outside quoted
// strings and comments, so the clause is found at the start of a line too
// (`SELECT …\nFROM t\nLIMIT 20`), which a plain substring search misses.
func hasTopLevelLimit(sqlText string) bool {
	return limitRe.MatchString(stripSQLNoise(sqlText))
}

// isPlainSelect reports whether the statement's first keyword is SELECT or
// WITH (a CTE). Auto-LIMIT must never touch INSERT/UPDATE/DELETE, SHOW or
// DDL, where an appended LIMIT is invalid or changes semantics.
func isPlainSelect(sqlText string) bool {
	s := strings.TrimSpace(stripSQLNoise(sqlText))
	end := 0
	for end < len(s) && (s[end] == '_' || s[end] >= 'a' && s[end] <= 'z' || s[end] >= 'A' && s[end] <= 'Z') {
		end++
	}
	switch strings.ToUpper(s[:end]) {
	case "SELECT", "WITH":
		return true
	}
	return false
}

// stripSQLNoise blanks the contents of quoted strings/identifiers and of
// -- / /* */ comments, replacing them with spaces so keyword scanning cannot
// match text inside literals.
func stripSQLNoise(s string) string {
	var b strings.Builder
	var q byte
	inLine, inBlock := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		case inBlock:
			if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				inBlock = false
				i++
				b.WriteString("  ")
			} else {
				b.WriteByte(' ')
			}
		case q != 0:
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteString("  ")
			} else {
				if c == q {
					q = 0
				}
				b.WriteByte(' ')
			}
		case c == '\'' || c == '"' || c == '`':
			q = c
			b.WriteByte(' ')
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			inLine = true
			i++
			b.WriteString("  ")
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			inBlock = true
			i++
			b.WriteString("  ")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// autoLimit appends "LIMIT n" to a plain single SELECT when the user picked a
// row cap and the dialect supports LIMIT (TablePlus auto-LIMIT behaviour).
func autoLimit(sqlText, driver string, n int) string {
	if n <= 0 {
		return sqlText
	}
	switch driver {
	case "mysql", "mariadb", "postgres", "postgresql", "sqlite":
	default:
		return sqlText // e.g. mssql: keep the user's statement untouched
	}
	trimmed := strings.TrimRight(sqlText, " \t\r\n;")
	if trimmed == "" || !isPlainSelect(trimmed) || hasTopLevelLimit(trimmed) {
		return sqlText
	}
	if limitBlockRe.MatchString(stripSQLNoise(trimmed)) || topRe.MatchString(stripSQLNoise(trimmed)) {
		return sqlText // FOR UPDATE / INTO OUTFILE / TOP already close the result
	}
	// Newline: a trailing "-- comment" must not swallow the clause.
	return trimmed + "\nLIMIT " + strconv.Itoa(n)
}
