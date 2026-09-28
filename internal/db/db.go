package db

import (
	"os"
	"path/filepath"
	"sync"
)

// exeDirPath is the directory of the running executable; the app keeps all
// of its state files (connections, history, ui layout) beside the binary.
func exeDirPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// StateFilePath names one state file beside the executable. Exported so the
// UI layer persists its own settings (ui.json) in the same directory.
func StateFilePath(name string) string {
	dir := exeDirPath()
	if dir == "" {
		return name
	}
	return filepath.Join(dir, name)
}

// Store is the data-layer kernel: saved connection definitions, live
// connections with their per-database pools, and the persisted statement
// history. Everything below the abstraction barrier operates on it; the UI
// owns nothing here and goes through the exported methods.
type Store struct {
	mu       sync.Mutex
	saved    []ConnConfig
	live     map[string]*Conn
	connFile string
	idSeq    int

	history     []HistoryEntry
	historySeq  int64
	historyFile string
}

// NewStore loads the persisted connections and history from beside the
// executable and seeds the connection-id sequence.
func NewStore() *Store {
	s := &Store{
		live:        make(map[string]*Conn),
		connFile:    StateFilePath("connections.json"),
		historyFile: StateFilePath("history.json"),
	}
	s.loadConnections()
	// Seed the ID sequence past any existing "c<n>" ids; see nextConnSeq.
	s.idSeq = nextConnSeq(s.saved)
	s.loadHistory()
	return s
}

// Saved returns a snapshot of the stored connection definitions.
func (c *Store) Saved() []ConnConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ConnConfig, len(c.saved))
	copy(out, c.saved)
	return out
}

// LiveCount reports how many connections are currently open.
func (c *Store) LiveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.live)
}

// CloseAll closes every live database pool before quitting.
func (c *Store) CloseAll() {
	c.mu.Lock()
	ids := make([]string, 0, len(c.live))
	for id := range c.live {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.Disconnect(id)
	}
}

// ReplaceHistory swaps the in-memory statement log and re-seeds the id
// sequence; the next persist writes exactly these entries.
func (c *Store) ReplaceHistory(entries []HistoryEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = append([]HistoryEntry(nil), entries...)
	var max int64
	for _, e := range c.history {
		if e.ID > max {
			max = e.ID
		}
	}
	c.historySeq = max
}

// History returns a snapshot of the statement log, oldest first.
func (c *Store) History() []HistoryEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]HistoryEntry, len(c.history))
	copy(out, c.history)
	return out
}
