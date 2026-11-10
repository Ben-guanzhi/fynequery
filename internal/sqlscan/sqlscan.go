// Package sqlscan is the ONE home for the client's lexical SQL scanning
// (tahboub18's one-pass philosophy, applied to the scanner fleet): keyword
// classification, the Safe Mode read-only verdict, noise stripping for
// keyword probes, and the two statement splitters (split.go).
//
// Inventory after the wave-49 convergence — what lives here and what
// deliberately does not:
//
//	sqlscan.IsReadOnly        — Safe Mode gate verdict (was safe_gate.go)
//	sqlscan.StripNoise        — blank literals/comments for keyword probes (was query_params.go)
//	sqlscan.IsPlainSelect     — autoLimit's first-keyword guard (was query_params.go)
//	sqlscan.HasTopLevelLimit  — existing LIMIT/OFFSET probe (was query_params.go)
//	sqlscan.HasForUpdateOrOutfile / HasSelectTop — autoLimit's trailing-clause guards
//	sqlscan.InQuotes          — shared by placeholder extraction and completion (was query_params.go)
//	sqlscan.StatementRanges   — editor script splitter, range form (was query.go)
//	sqlscan.SplitDelimited    — DELIMITER-aware splitter for restore (was backup.go)
//
// Deliberately standalone: internal/sqlkit.IsRowReturning classifies the
// TRANSPORT (Query vs Exec) for every engine path with its own first-word
// cut — merging it into FirstKeyword changes "(SELECT …)" and
// comment-led statements' routing, which deserves its own reviewed wave.
// The kernel's dbcore/sqltext.CteOuterIsRead is NOT reused for the gate: it
// inspects only the outermost WITH keyword, so a PostgreSQL data-modifying
// CTE body (WITH d AS (DELETE …) SELECT) reads as read — isReadOnly here
// additionally scans CTE body heads (see isWithStatementReadOnly).
package sqlscan

import (
	"regexp"
	"strings"
)

// readOnlyPrefixes are statement openers that never modify data.
var readOnlyPrefixes = []string{"SELECT", "SHOW", "EXPLAIN", "DESC", "DESCRIBE", "PRAGMA", "VALUES", "WITH", "TABLE"}

// FirstKeyword returns the statement's first keyword, skipping whitespace,
// -- line comments and /* block */ comments first. The word ends at the same
// separators sqlkit's transport classifier cuts at (space, tab, newline,
// semicolon, opening paren), so "SELECT(1)" yields "SELECT" and "SELECTED"
// stays whole. A leading comment no longer blinds callers — the Safe Mode
// gate used to read "-- note\nSELECT 1" as an unknown (write) statement.
func FirstKeyword(s string) string {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c == '-' && i+1 < len(s) && s[i+1] == '-' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			for i < len(s) {
				if s[i] == '*' && i+1 < len(s) && s[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		break
	}
	s = s[i:]
	end := 0
	for end < len(s) && s[end] != ' ' && s[end] != '\t' && s[end] != '\n' && s[end] != '\r' && s[end] != ';' && s[end] != '(' {
		end++
	}
	return strings.ToUpper(s[:end])
}

// IsReadOnly reports whether stmt is read-only — the Safe Mode gate's
// verdict (Alert Write / Password Write pass only read statements). The
// first keyword decides; WITH is special-cased: the CTE bodies are scanned
// past to find the main statement type AND each body's head keyword, so
// neither `WITH x AS (...) INSERT …` nor PostgreSQL's data-modifying CTE
// (`WITH d AS (DELETE FROM t RETURNING *) SELECT …`) slips through.
func IsReadOnly(stmt string) bool {
	switch FirstKeyword(stmt) {
	case "SELECT", "SHOW", "EXPLAIN", "DESC", "DESCRIBE", "PRAGMA", "VALUES", "TABLE":
		return true
	case "WITH":
		return isWithStatementReadOnly(stmt)
	}
	return false
}

// isWithStatementReadOnly scans past the CTE bodies to find the main
// statement type after the WITH clause. WITH x AS (...) SELECT is read-only;
// WITH x AS (...) INSERT/UPDATE/DELETE/CREATE/etc. is not.
//
// PostgreSQL additionally allows data-modifying statements INSIDE a CTE body
// (WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d really deletes), so
// the head word of each parenthesized body — the one directly following '(' —
// is checked for DML as well. Only that head word is checked, so a plain
// subquery (SELECT …) never reads as DML.
//
// At depth 0, CTE names are followed by AS — the main statement keyword is
// not. String literals (with ” and \' escapes), comments, and quoted
// identifiers are skipped.
func isWithStatementReadOnly(stmt string) bool {
	s := stmt
	i := 0
	// Skip trivia (whitespace/comments) up to and including "WITH" itself.
	for i < len(s) {
		c := s[i]
		if c == '-' && i+1 < len(s) && s[i+1] == '-' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			for i < len(s) {
				if s[i] == '*' && i+1 < len(s) && s[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		break
	}
	if !strings.HasPrefix(strings.ToUpper(s[i:]), "WITH") {
		return false // not a WITH statement after all; caller decides
	}
	i += 4

	// Skip optional RECURSIVE
	i = skipSpaces(s, i)
	if strings.HasPrefix(strings.ToUpper(s[i:]), "RECURSIVE") {
		i += len("RECURSIVE")
	}

	depth := 0
	parenFresh := false // the next word directly follows '('
	for i < len(s) {
		c := s[i]

		// Skip string literals (single quotes, with '' and \' escapes)
		if c == '\'' {
			i++
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					i += 2
					continue
				}
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			continue
		}

		// Skip line comments
		if c == '-' && i+1 < len(s) && s[i+1] == '-' {
			for i < len(s) && s[i] != '\n' && s[i] != '\r' {
				i++
			}
			continue
		}

		// Skip block comments
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
			continue
		}

		// Skip double-quote and backtick quoted identifiers
		if c == '"' || c == '`' {
			quote := c
			i++
			for i < len(s) {
				if s[i] == quote {
					if i+1 < len(s) && s[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			continue
		}

		// Skip bracket-quoted identifiers (SQL Server)
		if c == '[' {
			i++
			for i < len(s) && s[i] != ']' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}

		if c == '(' {
			depth++
			parenFresh = true
			i++
			continue
		}
		if c == ')' {
			depth--
			i++
			continue
		}

		// Skip separators and whitespace at any depth
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == ';' {
			i++
			continue
		}

		// Read a word at any depth
		start := i
		for i < len(s) && isWordByte(s[i]) {
			i++
		}
		word := strings.ToUpper(s[start:i])
		fresh := parenFresh
		parenFresh = false

		// Data-modifying CTE body (PostgreSQL): WITH d AS (DELETE FROM t
		// RETURNING *) SELECT * FROM d really deletes, at depth > 0 — the
		// depth-0 keyword alone is not the whole story.
		if fresh && isDataModifyingKeyword(word) {
			return false
		}

		if depth > 0 {
			if word == "" {
				i++ // punctuation inside a CTE body: skip one char
			}
			continue
		}

		// At depth 0: skip the AS keyword itself (shouldn't appear standalone,
		// but guard)
		if word == "AS" {
			i = skipSpaces(s, i)
			continue
		}

		// Check if this word is followed by AS → CTE name, skip past it
		j := skipSpaces(s, i)
		if strings.HasPrefix(strings.ToUpper(s[j:]), "AS") {
			i = j + 2
			continue
		}

		// This is the main statement type
		switch word {
		case "SELECT", "SHOW", "EXPLAIN", "DESC", "DESCRIBE", "VALUES", "TABLE":
			return true
		}
		return false
	}
	return true
}

// isDataModifyingKeyword reports whether word starts a DML statement. Used for
// the heads of CTE bodies, where PostgreSQL permits INSERT/UPDATE/DELETE (with
// RETURNING) inside a WITH that otherwise looks read-only; REPLACE and MERGE
// are the MySQL / MSSQL-Oracle spellings of the same hazard.
func isDataModifyingKeyword(word string) bool {
	switch word {
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE":
		return true
	}
	return false
}

func skipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

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

// HasTopLevelLimit reports whether the noise-stripped statement already
// carries a LIMIT or OFFSET keyword anywhere at statement level.
func HasTopLevelLimit(sqlText string) bool {
	return limitRe.MatchString(StripNoise(sqlText))
}

// HasForUpdateOrOutfile reports whether the statement ends in a clause that
// must stay last (FOR UPDATE/SHARE, LOCK IN SHARE MODE, INTO OUTFILE/DUMPFILE)
// — appending LIMIT after any of them is rejected by the server.
func HasForUpdateOrOutfile(sqlText string) bool {
	return limitBlockRe.MatchString(sqlText)
}

// HasSelectTop reports whether the statement already caps itself with the SQL
// Server TOP form (SELECT [DISTINCT] TOP n …).
func HasSelectTop(sqlText string) bool {
	return topRe.MatchString(sqlText)
}

// IsPlainSelect reports whether the statement's first keyword is SELECT or
// WITH (a CTE). Auto-LIMIT must never touch INSERT/UPDATE/DELETE, SHOW or
// DDL, where an appended LIMIT is invalid or changes semantics.
func IsPlainSelect(sqlText string) bool {
	s := strings.TrimSpace(StripNoise(sqlText))
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

// StripNoise blanks the contents of quoted strings/identifiers and of
// -- / /* */ comments, replacing them with spaces so keyword scanning cannot
// match text inside literals.
func StripNoise(s string) string {
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

// InQuotes reports whether pos falls inside a 'single' or "double" quoted
// span (placeholders there are literal text, not parameters).
func InQuotes(s string, pos int) bool {
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
