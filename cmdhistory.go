package main

// Command-history persistence: every executed statement (SQL and KV commands
// alike) is recorded to disk — tagged with the connection it ran on — so the
// History pane, its fuzzy search and Ctrl+Up/Ctrl+Down navigation survive a
// restart. The file is one JSON list, capped at maxCommandHistory entries,
// written atomically (tmp + rename) so a crash mid-write cannot leave a
// half-written file.
//
// Each entry carries the connection name it ran on, so the global history is
// genuinely cross-connection: the user can tell which connection a command
// belonged to and search within one. Statements are stored verbatim; this
// matches the pending-edit journal's plaintext-on-disk policy (a connection's
// secrets live in connections.json, not here).

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

// historyRec is one executed statement plus the connection it ran on.
type historyRec struct {
	Stmt string `json:"stmt"`
	Conn string `json:"conn,omitempty"`
}

// commandHistoryFile is the on-disk shape: one JSON list of records, oldest
// first (the order they were run).
type commandHistoryFile struct {
	Entries []historyRec `json:"entries"`
}

// loadCommandHistory reads the persisted history. A missing or corrupt file
// yields nil — the session just starts with an empty history, never an error
// dialog (history is a convenience, not something worth nagging about). A
// legacy file (a bare list of statements, pre-attribution) loads with an empty
// connection label.
func loadCommandHistory() []historyRec {
	raw, err := os.ReadFile(commandHistoryPath())
	if err != nil {
		return nil
	}
	var f commandHistoryFile
	if err := json.Unmarshal(raw, &f); err == nil {
		return f.Entries
	}
	var legacy []string
	if err := json.Unmarshal(raw, &legacy); err == nil {
		out := make([]historyRec, 0, len(legacy))
		for _, s := range legacy {
			out = append(out, historyRec{Stmt: s})
		}
		return out
	}
	return nil
}

// saveCommandHistory writes the history atomically (tmp + rename) to path.
// The path is resolved by the caller and passed in: the write runs on a
// background goroutine, so it must not re-read configDir() (an env override
// set for a test may already have been restored by the time it lands).
func saveCommandHistory(path string, entries []historyRec) error {
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
