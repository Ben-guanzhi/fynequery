package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isolates the whole suite from the user's real state. State files now
// live in the shared config directory, so a test that runs a query would
// otherwise append to (and rewrite) the history of the person who ran it.
// TABLEPRO_STATE_DIR sends every state file the tests touch into a temp
// folder instead.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fynequery-state")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: %v\n", err)
		os.Exit(1)
	}
	os.Setenv("TABLEPRO_STATE_DIR", dir)
	code := m.Run()
	os.Unsetenv("TABLEPRO_STATE_DIR")
	os.RemoveAll(dir)
	os.Exit(code)
}
