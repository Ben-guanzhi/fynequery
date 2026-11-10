package main

// Command-history persistence: every executed statement (SQL and KV commands
// alike) is recorded to disk so the History pane — and Ctrl+Up/Ctrl+Down
// navigation — survive a restart. The file is one JSON list, capped at
// maxCommandHistory entries, written atomically (tmp + rename) so a crash
// mid-write cannot leave a half-written file.
//
// Entries are stored verbatim (they are the statements the user ran). This
// matches the pending-edit journal's plaintext-on-disk policy; a connection's
// secrets live in connections.json, not here.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// maxCommandHistory caps the on-disk (and in-memory) history so the file
// cannot grow without bound over months of use. Oldest entries are dropped
// first, like every command console.
const maxCommandHistory = 1000

// commandHistoryPath is where the executed-statement history lives.
func commandHistoryPath() string {
	return filepath.Join(configDir(), "command-history.json")
}

// commandHistoryFile is the on-disk shape: one JSON list of statements, oldest
// first (the order they were run).
type commandHistoryFile struct {
	Entries []string `json:"entries"`
}

// loadCommandHistory reads the persisted history. A missing or corrupt file
// yields nil — the session just starts with an empty history, never an error
// dialog (history is a convenience, not something worth nagging about).
func loadCommandHistory() []string {
	raw, err := os.ReadFile(commandHistoryPath())
	if err != nil {
		return nil
	}
	var f commandHistoryFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	return f.Entries
}

// saveCommandHistory writes the history atomically (tmp + rename) to path.
// The path is resolved by the caller and passed in: the write runs on a
// background goroutine, so it must not re-read configDir() (an env override
// set for a test may already have been restored by the time it lands).
func saveCommandHistory(path string, entries []string) error {
	raw, err := json.MarshalIndent(commandHistoryFile{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// historyWriter serializes the history's disk writes and drops superseded
// ones. appendHistory fires on every executed statement — potentially a long
// Run All — and only the LATEST snapshot must end up on disk: writers
// serialize on mu, and a writer whose generation is no longer current bails,
// so an older task can never finish after a newer one and overwrite it with
// stale content (same shape as the pending-edit journal writer).
type historyWriter struct {
	mu  sync.Mutex
	gen uint64
}
