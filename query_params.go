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

	"github.com/dbcore/dbcore/sqltext"

	"fynequery/internal/sqlscan"
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
	if err := json.Unmarshal(raw, &m); err != nil {
		appLogf("query vars parse: %v", err)
	}
	return m
}

// saveQueryVars persists the remembered values; failures are logged, not
// fatal (the dialog still works — only the "remember" is lost).
func saveQueryVars(m map[string]string) {
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		appLogf("query vars save: %v", err)
		return
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		appLogf("query vars save: %v", err)
		return
	}
	if err := os.WriteFile(varFile(), raw, 0o600); err != nil {
		appLogf("query vars save: %v", err)
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

// extractParams scans a statement and returns the placeholder names in order
// of first appearance (duplicates collapse to one entry; positional ? is
// named ?1, ?2, …).
func extractParams(sqlText string) []string {
	var names []string
	seen := map[string]bool{}
	positional := 0
	for _, m := range paramRe.FindAllStringSubmatchIndex(sqlText, -1) {
		start := m[0]
		if sqlscan.InQuotes(sqlText, start) {
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
		if sqlscan.InQuotes(sqlText, start) {
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

// resultNameRe matches a "-- name: …" / "-- label: …" / "-- tab: …" comment.
var resultNameRe = regexp.MustCompile(`(?m)^\s*--\s*(?:name|label|tab)\s*:\s*(.+)$`)

// resultName extracts that comment from the statement (TablePlus uses it to
// label the result tab); "" when absent.
func resultName(sqlText string) string {
	if m := resultNameRe.FindStringSubmatch(sqlText); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
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
	if trimmed == "" || !sqlscan.IsPlainSelect(trimmed) || sqlscan.HasTopLevelLimit(trimmed) {
		return sqlText
	}
	// A WITH statement is only capped when its outermost statement (the
	// keyword at paren depth 0 after the CTE list) is a read: appending LIMIT
	// to WITH ... INSERT truncates the write. The verdict comes from the
	// kernel so both implementations rule identically (wave 40 fixed the
	// kernel's own LIMIT arm for the same bug). The stripped text is
	// TrimSpace'd first: stripSQLNoise blanks leading "-- name:" comments
	// into spaces, and an untrimmed HasPrefix("WITH") would skip the guard
	// for every statement carrying one — re-opening the truncation hole.
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sqlscan.StripNoise(trimmed))), "WITH") && !sqltext.CteOuterIsRead(trimmed) {
		return sqlText
	}
	// One noise-stripped copy serves both probes (FOR UPDATE / INTO OUTFILE /
	// TOP); stripping is a full-text copy, doing it twice was pure waste.
	noise := sqlscan.StripNoise(trimmed)
	if sqlscan.HasForUpdateOrOutfile(noise) || sqlscan.HasSelectTop(noise) {
		return sqlText // FOR UPDATE / INTO OUTFILE / TOP already close the result
	}
	// Newline: a trailing "-- comment" must not swallow the clause.
	return trimmed + "\nLIMIT " + strconv.Itoa(n)
}
