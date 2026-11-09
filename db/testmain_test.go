package db

import (
	"os"
	"testing"
)

// TestMain keeps side-effecting stores out of the real user config: the SSH
// TOFU store must never record test keys in ssh-known-hosts.json, and since
// every test run generates fresh host keys, a persisted entry from an earlier
// run would fail the next run's dial as a "key changed" attack warning.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fynequery-db-tests")
	if err != nil {
		os.Exit(1)
	}
	SetKnownHostsPath(dir + string(os.PathSeparator) + "ssh-known-hosts.json")
	// os.Exit never runs defers — clean up BEFORE exiting or every test run
	// leaks one temp directory.
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
