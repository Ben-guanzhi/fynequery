package db

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHistoryWriteIsThrottled pins what the statement log costs on disk: the
// first entry goes out immediately, one recorded inside the delay stays in
// memory until a flush, and a flush with nothing pending rewrites nothing.
// Quitting is that flush (see Client.closeAll), so a session's tail is written
// when the window closes instead of once per statement.
func TestHistoryWriteIsThrottled(t *testing.T) {
	file := filepath.Join(t.TempDir(), "history.json")
	s := &Store{live: make(map[string]*Conn), historyFile: file}

	s.RecordStatement("c0", "db", "SELECT 1", QueryResult{})
	if got := readHistoryFile(t, file); len(got) != 1 {
		t.Fatalf("on-disk entries after the first statement = %d, want 1", len(got))
	}

	s.RecordStatement("c0", "db", "SELECT 2", QueryResult{})
	if got := readHistoryFile(t, file); len(got) != 1 {
		t.Fatalf("on-disk entries inside the delay = %d, want the second one still pending", len(got))
	}
	if len(s.History()) != 2 {
		t.Fatalf("in-memory entries = %d, want 2 (a throttled write is not a lost one)", len(s.History()))
	}

	s.PersistHistory()
	got := readHistoryFile(t, file)
	if len(got) != 2 || got[1].SQL != "SELECT 2" {
		t.Fatalf("after the flush = %+v, want both statements", got)
	}

	// Nothing pending means nothing to rewrite.
	if err := os.Remove(file); err != nil {
		t.Fatalf("remove: %v", err)
	}
	s.PersistHistory()
	if _, err := os.Stat(file); err == nil {
		t.Fatal("an unmodified log was rewritten")
	}
}

func readHistoryFile(t *testing.T, file string) []HistoryEntry {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read: %v", err)
	}
	var entries []HistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return entries
}
