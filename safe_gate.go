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
func isReadOnlySQL(stmt string) bool {
	upper := strings.ToUpper(trimLeading(stmt))
	for _, p := range readOnlyPrefixes {
		if upper == p || strings.HasPrefix(upper, p+" ") || strings.HasPrefix(upper, p+"\n") || strings.HasPrefix(upper, p+"(") {
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
// does nothing and the caller must treat the send as cancelled.
func gateSafeMode(w fyne.Window, conn Connection, stmt string, proceed func()) {
	if !safeGateBlocked(conn, stmt) {
		proceed()
		return
	}
	confirmSafeMode(w, conn, stmt, func(ok bool) {
		if !ok {
			return
		}
		if conn.SafeMode == 3 || conn.SafeMode == 4 {
			confirmPassword(w, conn, func(match bool) {
				if match {
					proceed()
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
