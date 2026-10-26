package main

// Saved queries (TablePlus "Queries" tab): named SQL statements persisted in
// queries.json, each optionally bound to keywords. Typing a keyword in the
// editor offers the whole query in the completion menu; the sidebar's 查询
// pane lists them for run / copy / delete.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SavedQuery is one entry of the saved-query panel.
type SavedQuery struct {
	Name     string   `json:"name"`
	Keywords []string `json:"keywords,omitempty"`
	SQL      string   `json:"sql"`
}

func savedQueriesFile() string { return filepath.Join(configDir(), "queries.json") }

// savedQueriesPath is the storage location; a package var so tests can point
// it at a temp dir.
var savedQueriesPath = savedQueriesFile()

// loadSavedQueries reads the panel data (empty list when missing/corrupt).
func loadSavedQueries() []SavedQuery {
	raw, err := os.ReadFile(savedQueriesPath)
	if err != nil {
		return nil
	}
	var out []SavedQuery
	_ = json.Unmarshal(raw, &out)
	return out
}

func saveSavedQueries(qs []SavedQuery) error {
	if len(qs) == 0 {
		return os.Remove(savedQueriesPath)
	}
	raw, err := json.MarshalIndent(qs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(savedQueriesPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(savedQueriesPath, raw, 0o600)
}

// upsertSavedQuery inserts or replaces by name and persists.
func upsertSavedQuery(q SavedQuery) error {
	qs := loadSavedQueries()
	seen := false
	for i := range qs {
		if qs[i].Name == q.Name {
			qs[i] = q
			seen = true
			break
		}
	}
	if !seen {
		qs = append(qs, q)
	}
	return saveSavedQueries(qs)
}

// deleteSavedQuery removes by name and persists.
func deleteSavedQuery(name string) error {
	qs := loadSavedQueries()
	out := qs[:0]
	for _, q := range qs {
		if q.Name != name {
			out = append(out, q)
		}
	}
	return saveSavedQueries(out)
}

// savedQueryKeywords flattens the keyword bindings for the completion menu.
// Cached on the file's mtime+size: the Ctrl+Space completion path used to
// re-read and re-parse queries.json per keystroke.
func savedQueryKeywords() map[string][]SavedQuery {
	var modTime int64
	var size int64
	if st, err := os.Stat(savedQueriesPath); err == nil {
		modTime, size = st.ModTime().UnixNano(), st.Size()
	}
	savedQueryMu.Lock()
	defer savedQueryMu.Unlock()
	if savedQueryKws != nil && savedQueryMod == modTime && savedQuerySize == size {
		return savedQueryKws
	}
	out := map[string][]SavedQuery{}
	for _, q := range loadSavedQueries() {
		for _, kw := range q.Keywords {
			out[strings.ToLower(kw)] = append(out[strings.ToLower(kw)], q)
		}
	}
	savedQueryMod, savedQuerySize, savedQueryKws = modTime, size, out
	return out
}

var (
	savedQueryMu   sync.Mutex
	savedQueryMod  int64
	savedQuerySize int64
	savedQueryKws  map[string][]SavedQuery
)

// sortedSavedQueries returns the panel list in stable name order.
func sortedSavedQueries() []SavedQuery {
	qs := loadSavedQueries()
	sort.SliceStable(qs, func(i, j int) bool { return qs[i].Name < qs[j].Name })
	return qs
}
