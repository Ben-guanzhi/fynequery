package db

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// stateDirEnv overrides the state directory. Tests point it at a temp folder so
// a run can never rewrite the user's real saved connections, and a portable
// install can aim it at the app folder.
const stateDirEnv = "TABLEPRO_STATE_DIR"

// exeDirPath is the directory of the running executable. Releases before the
// config-directory layout kept all state files here, so it is also where the
// one-time migration looks.
func exeDirPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// stateDir is where the app keeps its state files (connections, history, ui
// layout): the same config/TableProGo directory the Wails version writes, so
// saved connections are shared between the two clients instead of credentials
// sitting in a source tree beside the binary. Without an OS config directory
// the old beside-the-exe layout is kept rather than refusing to run.
func stateDir() string {
	if dir := os.Getenv(stateDirEnv); dir != "" {
		return dir
	}
	if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
		return filepath.Join(cfg, "TableProGo")
	}
	return exeDirPath()
}

// StateFilePath names one state file. Exported so the UI layer persists its own
// settings (ui.json) in the same directory.
func StateFilePath(name string) string {
	return resolveStateFile(stateDir(), exeDirPath(), name)
}

// resolveStateFile answers where `name` lives for this launch and, on the first
// launch after the layout change, copies the beside-the-exe file into dir.
// The legacy copy stays put: deleting user data on startup is not this
// program's call. dir is created because every caller writes the file back.
func resolveStateFile(dir, exeDir, name string) string {
	if dir == "" {
		// No config directory and no executable directory: name the file
		// relatively and let the caller's write report the problem.
		return name
	}
	target := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return target
	}
	if _, err := os.Stat(target); err == nil {
		return target
	}
	if exeDir == "" || exeDir == dir {
		return target
	}
	legacy := filepath.Join(exeDir, name)
	data, err := os.ReadFile(legacy)
	if err != nil {
		return target
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		log.Printf("Failed to migrate %s: %v", legacy, err)
		return target
	}
	log.Printf("Migrated %s to %s", legacy, target)
	return target
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
	// histObserved is the history file as this process last read or wrote it;
	// mergeHistoryLocked folds in only what other clients added since then.
	histObserved []HistoryEntry
	// connObserved is the connections file as this process last read or wrote
	// it; persist folds in only what other clients added since then.
	connObserved []ConnConfig
	// historyDirty marks entries changed but not yet on disk; historyFlushed is
	// when they last were, which is what the write delay is measured from.
	historyDirty   bool
	historyFlushed time.Time
	// historyTimer is the trailing flush of a throttled burst: it fires when
	// the delay has passed with the log still dirty, so the tail reaches disk
	// even if no further statement runs and the app never gets a clean quit
	// (crash, power loss).
	historyTimer *time.Timer

	// favorites is the saved-statement list. It is this app's own file, so
	// unlike history there is no cross-client merge and no write throttle.
	favorites     []FavoriteEntry
	favSeq        int64
	favoritesFile string
}

// NewStore loads the persisted connections and history from beside the
// executable and seeds the connection-id sequence.
func NewStore() *Store {
	s := &Store{
		live:          make(map[string]*Conn),
		connFile:      StateFilePath("connections.json"),
		historyFile:   StateFilePath("history.json"),
		favoritesFile: StateFilePath("favorites.json"),
	}
	s.loadConnections()
	// Seed the ID sequence past any existing "c<n>" ids; see nextConnSeq.
	s.idSeq = nextConnSeq(s.saved)
	s.loadHistory()
	s.loadFavorites()
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
	c.history = append([]HistoryEntry(nil), entries...)
	c.historyDirty = true
	var max int64
	for i := range c.history {
		e := &c.history[i]
		if e.ID > max {
			max = e.ID
		}
		e.fillLowerCache()
	}
	c.historySeq = max
	c.mu.Unlock()
	// A replace is authoritative: write exactly these entries and do NOT merge
	// the on-disk log back in. Otherwise, with historySeq reset to 0 here, the
	// next persist would re-add stale entries already on disk under reused low
	// ids. Tests reset in-memory history this way, and a shared history.json can
	// carry entries from other tests in the same process, so the swap must clear
	// the file rather than let the merge re-inject them.
	c.persistHistoryNow(false)
}

// History returns a snapshot of the statement log, oldest first.
func (c *Store) History() []HistoryEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]HistoryEntry, len(c.history))
	copy(out, c.history)
	return out
}
