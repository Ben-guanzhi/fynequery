package db

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The state files are shared with the parent TableProGo client. These tests
// pin the cross-client merge: additions from the other client survive a
// rewrite and are adopted into memory, and this process's own deletions stick.

func TestRecordKeepsOtherClientsStatements(t *testing.T) {
	file := filepath.Join(t.TempDir(), "history.json")
	a := &Store{live: make(map[string]*Conn), historyFile: file}
	a.RecordStatement("c1", "zs", "select 1", QueryResult{})

	b := &Store{live: make(map[string]*Conn), historyFile: file}
	b.loadHistory()
	b.RecordStatement("c2", "zs", "select 2", QueryResult{})

	// The second statement lands inside the throttle window, so the merge and
	// flush have to be asked for explicitly.
	a.RecordStatement("c1", "zs", "select 3", QueryResult{})
	a.PersistHistory()

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	var entries []HistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse history: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("statements after merge = %d, want 3: %s", len(entries), data)
	}
	ids := map[int64]bool{}
	for _, e := range entries {
		if ids[e.ID] {
			t.Fatalf("duplicate history id %d after merge", e.ID)
		}
		ids[e.ID] = true
	}
}

func TestPersistKeepsForeignAddsAndOwnDeletes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "connections.json")

	a := &Store{live: make(map[string]*Conn), connFile: file}
	a.saved = []ConnConfig{{ID: "c0", Name: "one", Type: DbTypeSQLite, Database: "a.db"}}
	a.persist()

	b := &Store{live: make(map[string]*Conn), connFile: file}
	b.loadConnections()
	b.saved = append(b.saved, ConnConfig{
		ID: "c1", Name: "two", Type: DbTypePostgres, Host: "h", Port: 5432, Database: "b",
	})
	b.persist()

	// a saves again: b's connection must survive the rewrite and be adopted
	// into a's memory, so a's next save keeps it too.
	a.saved = append(a.saved, ConnConfig{
		ID: "c2", Name: "three", Type: DbTypeMySQL, Host: "h2", Port: 3306, Database: "c",
	})
	a.persist()
	if len(a.saved) != 3 {
		t.Fatalf("the foreign connection was not adopted into memory: %d", len(a.saved))
	}
	var file1 []ConnConfig
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read connections: %v", err)
	}
	if err := json.Unmarshal(data, &file1); err != nil {
		t.Fatalf("parse connections: %v", err)
	}
	if len(file1) != 3 {
		t.Fatalf("connections after merge = %d, want 3: %s", len(file1), data)
	}

	// a deletes its own first connection: the deletion must stick instead of
	// resurrecting from the file it was just removed from.
	a.saved = a.saved[1:]
	a.persist()
	data, err = os.ReadFile(file)
	if err != nil {
		t.Fatalf("reread connections: %v", err)
	}
	file1 = nil
	if err := json.Unmarshal(data, &file1); err != nil {
		t.Fatalf("parse connections: %v", err)
	}
	// The adopted connection sits at the end of a's list, so compare by name
	// set rather than position.
	if len(file1) != 2 {
		t.Fatalf("saved connections = %s, want two entries", data)
	}
	names := map[string]bool{}
	for _, cfg := range file1 {
		names[cfg.Name] = true
	}
	if !names["two"] || !names["three"] {
		t.Fatalf("saved connections = %s, want [two three]", data)
	}
}
