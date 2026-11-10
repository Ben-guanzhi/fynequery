package main

// Safe Mode (TablePlus "Safe Mode" five levels): a per-connection gate that
// asks for confirmation — or re-enters the database password — before
// statements are sent to the server, protecting against accidental data
// changes.
//
//	0 Silent   — send everything, no prompts (default)
//	1 Alert 1  — confirm every statement
//	2 Alert 2  — confirm non read-only statements
//	3 Safe 1   — require the DB password before every statement
//	4 Safe 2   — require the DB password before non read-only statements

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// safeModeNames lists the dropdown options for the connection form.
var safeModeNames = []string{"Silent", "Alert All", "Alert Write", "Password All", "Password Write"}

// safeModeForName maps a dropdown label to its level.
func safeModeForName(label string) int {
	switch label {
	case "Alert All":
		return 1
	case "Alert Write":
		return 2
	case "Password All":
		return 3
	case "Password Write":
		return 4
	default:
		return 0
	}
}

// safeModeName maps a level back to its label.
func safeModeName(level int) string {
	if level < 0 || level > 4 {
		return "Silent"
	}
	return safeModeNames[level]
}

// readOnlyPrefixes are statement openers that never modify data.
var readOnlyPrefixes = []string{"SELECT", "SHOW", "EXPLAIN", "DESC", "DESCRIBE", "PRAGMA", "VALUES", "WITH", "TABLE"}

// isReadOnlySQL reports whether a trimmed statement is read-only.
// WITH statements are special-cased: the CTE bodies are scanned past to
// find the main statement type (SELECT = read-only, INSERT/UPDATE/DELETE
// = write). Without this, `WITH x AS (...) INSERT INTO ...` bypasses
// Alert Write / Password Write protection because it starts with WITH.
func isReadOnlySQL(stmt string) bool {
	upper := strings.ToUpper(trimLeading(stmt))
	for _, p := range readOnlyPrefixes {
		if upper == p || strings.HasPrefix(upper, p+" ") || strings.HasPrefix(upper, p+"\n") || strings.HasPrefix(upper, p+"(") {
			if p == "WITH" {
				return isWithStatementReadOnly(stmt)
			}
			return true
		}
	}
	return false
}

func trimLeading(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	return s
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
	i := 4 // skip "WITH"

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
		return isReadOnlyKeyword(word)
	}
	return true
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

func isReadOnlyKeyword(word string) bool {
	switch word {
	case "SELECT", "SHOW", "EXPLAIN", "DESC", "DESCRIBE", "VALUES", "TABLE":
		return true
	}
	return false
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

// safeGateBlocked reports whether this connection's safe mode requires a
// prompt for the given statement (false = send without ceremony).
func safeGateBlocked(conn Connection, stmt string) bool {
	switch conn.SafeMode {
	case 1, 3:
		return true
	case 2, 4:
		return !isReadOnlySQL(stmt)
	}
	return false
}

// gateSafeMode checks the connection's safe mode for one statement. When the
// statement is allowed (possibly after a prompt the user accepted) it runs
// proceed on the UI goroutine; when declined or the password check fails it
// runs the optional cancel callback (default no-op) so the caller can reset
// any run state it had armed before the gate.
func gateSafeMode(w fyne.Window, conn Connection, stmt string, proceed func(), cancel ...func()) {
	onCancel := func() {}
	if len(cancel) > 0 && cancel[0] != nil {
		onCancel = cancel[0]
	}
	if !safeGateBlocked(conn, stmt) {
		proceed()
		return
	}
	confirmSafeMode(w, conn, stmt, func(ok bool) {
		if !ok {
			onCancel()
			return
		}
		if conn.SafeMode == 3 || conn.SafeMode == 4 {
			confirmPassword(w, conn, func(match bool) {
				if match {
					proceed()
				} else {
					onCancel()
				}
			})
			return
		}
		proceed()
	})
}

// confirmSafeMode asks for explicit confirmation to send the statement.
func confirmSafeMode(w fyne.Window, conn Connection, stmt string, done func(bool)) {
	dialog.NewConfirm("Safe Mode — "+conn.Name,
		"Send this statement to the server?\n\n"+truncateForDialog(stmt),
		done, w).Show()
}

// confirmPassword re-asks for the connection's database password (TablePlus
// uses Touch ID here; a plain password field in fynequery). Connections
// without a stored password skip the check with a notice.
func confirmPassword(w fyne.Window, conn Connection, done func(bool)) {
	if conn.Password == "" {
		dialog.NewInformation("Safe Mode",
			"No password is stored for this connection; skipping the password check.", w).Show()
		done(true)
		return
	}
	entry := widget.NewPasswordEntry()
	entry.SetPlaceHolder("database password")
	var d dialog.Dialog
	apply := widget.NewButton("Confirm", func() {
		match := entry.Text == conn.Password
		d.Hide()
		if !match {
			dialog.NewInformation("Safe Mode", "Password did not match; statement not sent.", w).Show()
		}
		done(match)
	})
	apply.Importance = widget.HighImportance
	cancel := widget.NewButton("Cancel", func() {
		d.Hide()
		done(false)
	})
	d = dialog.NewCustom("Safe Mode — confirm password", "",
		container.NewVBox(
			entry,
			container.NewBorder(nil, nil, nil, apply, cancel),
		), w)
	d.Resize(fyne.NewSize(420, 140))
	d.Show()
}

// truncateForDialog shortens s for dialog display.
func truncateForDialog(s string) string {
	const n = 500
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
