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
// LIMIT/OFFSET/FETCH clause that should not be wrapped.
func hasTopLevelLimit(sqlText string) bool {
	s := strings.ToLower(sqlText)
	return strings.Contains(s, " limit ") || strings.Contains(s, " offset ") ||
		strings.Contains(s, " fetch ") || strings.Contains(s, " top ")
}

// autoLimit appends "LIMIT n" to a plain single SELECT when the user picked a
// row cap and the dialect supports LIMIT (TablePlus auto-LIMIT behaviour).
func autoLimit(sqlText, driver string, n int) string {
	if n <= 0 || hasTopLevelLimit(sqlText) {
		return sqlText
	}
	switch driver {
	case "mysql", "mariadb", "postgres", "postgresql", "sqlite":
	default:
		return sqlText // e.g. mssql: keep the user's statement untouched
	}
	trimmed := strings.TrimRight(sqlText, " ;\t\n")
	return trimmed + " LIMIT " + strconv.Itoa(n)
}
