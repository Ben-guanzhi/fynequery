package main

import (
	"os"
	"testing"
)

// TestMain disables the delayed editor focus for the whole test binary.
//
// bindEditorShortcuts schedules that focus with time.AfterFunc(80ms). The fyne
// test driver runs fyne.Do callbacks inline on the calling goroutine — there
// is no UI thread to serialize against — so the callback would execute
// concurrently with whichever test goroutine is mid-UI-cascade when it fires
// (opening a query tab triggers a deep renderer/refresh cascade that easily
// outlasts 80ms under -race), and such timers outlive the test that scheduled
// them, leaking into unrelated later tests.
//
// Production behavior is unchanged; tests that need a focused editor focus it
// explicitly (see TestAutoCompleteShowsSuggestions).
func TestMain(m *testing.M) {
	delayedFocusSuppressed = true
	os.Exit(m.Run())
}
