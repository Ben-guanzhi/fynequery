package main

// Privacy Mode (Beekeeper Studio "Toggle Privacy Mode"): one switch that
// redacts host / port / database names everywhere they are displayed, so a
// shared screen or demo recording does not leak where the database lives.

// privacyMode is the global redaction switch (off by default).
var privacyMode bool

// togglePrivacyMode flips the switch and reports the new state.
func togglePrivacyMode() bool {
	privacyMode = !privacyMode
	return privacyMode
}

// privacyOn reports whether redaction is active.
func privacyOn() bool { return privacyMode }

// maskedPart returns the value, or the redaction mark while privacy mode is
// on. Empty values stay empty.
func maskedPart(s string) string {
	if privacyMode && s != "" {
		return "•••"
	}
	return s
}
