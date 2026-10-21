package db

import (
	"encoding/json"
	"log"
	"strings"
	"time"
)

// FavoriteEntry is one statement the user saved on purpose, mirroring
// TablePlus's "Favorite" half of its history panel: history is what you ran,
// favorites are what you meant to run again.
type FavoriteEntry struct {
	ID       int64  `json:"id"`
	Time     int64  `json:"time"`
	Name     string `json:"name,omitempty"`   // optional label; the UI falls back to the SQL's first line
	ConnID   string `json:"connId,omitempty"` // kept for a future per-connection filter, never shown
	Database string `json:"database,omitempty"`
	SQL      string `json:"sql"`

	// lowerSQL/lowerName are the precomputed lowercase caches the filter box
	// matches against and favoriteKey de-duplicates on; see HistoryEntry.
	lowerSQL, lowerName string
}

// favoritesLimit keeps the on-disk list bounded: saving the 201st drops the
// oldest, which is the one least likely to still be wanted.
const favoritesLimit = 200

// favoritesSQLChars truncates a stored statement so one huge paste cannot
// bloat the file, the same guard history uses.
const favoritesSQLChars = 4000

// fillLowerCache fills the filter and de-duplication caches; called once per
// entry on load and on save.
func (e *FavoriteEntry) fillLowerCache() {
	e.lowerSQL = strings.ToLower(e.SQL)
	e.lowerName = strings.ToLower(e.Name)
}

// favoriteKey collapses whitespace and case so re-favouriting the same
// statement after a reformat - or after a Ctrl+Shift+F pass - hits the same
// entry instead of piling up a near-duplicate.
func favoriteKey(sql string) string {
	return strings.ToLower(strings.Join(strings.Fields(sql), " "))
}

// MatchesFilter reports whether the entry matches the (already-lowercased)
// filter across the statement, its label and its database.
func (e *FavoriteEntry) MatchesFilter(lowerFilter string) bool {
	return strings.Contains(e.lowerSQL, lowerFilter) ||
		strings.Contains(e.lowerName, lowerFilter) ||
		strings.Contains(strings.ToLower(e.Database), lowerFilter)
}

// loadFavorites reads the persisted list.
func (c *Store) loadFavorites() {
	entries, ok, err := readJSONFile[FavoriteEntry](c.favoritesFile)
	if err != nil {
		log.Printf("Failed to load favorites: %v", err)
		return
	}
	if !ok {
		return
	}
	c.favorites = entries
	for i := range entries {
		e := &entries[i]
		if e.ID > c.favSeq {
			c.favSeq = e.ID
		}
		e.fillLowerCache()
	}
	log.Printf("Loaded %d favorite entries from %s", len(entries), c.favoritesFile)
}

// Favorites returns a snapshot of the list in the order it was saved - oldest
// first, the same shape History() answers with. The sidebar shows them newest
// first, and it is that list's job to reverse them (see entryList.refilter),
// so a second caller that wants the file's order is not surprised by it.
func (c *Store) Favorites() []FavoriteEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]FavoriteEntry, len(c.favorites))
	copy(out, c.favorites)
	return out
}

// SaveFavorite records the current statement. added is false when the
// statement is already favourited: the existing entry is returned unchanged so
// its position in the list does not jump, and the caller can say so instead of
// silently pretending it saved something. An empty statement is refused the
// same way - there is nothing to load back.
//
// The write goes straight to disk rather than through history's 5-second
// throttle: favouriting is a deliberate, once-in-a-while click, and the
// throttle exists only because every executed statement used to rewrite 500
// log entries. Borrowing it here would buy a state machine and a small window
// in which a crash forgets a click the user just made.
func (c *Store) SaveFavorite(connID, database, query string) (FavoriteEntry, bool) {
	raw := strings.TrimSpace(query)
	if raw == "" {
		return FavoriteEntry{}, false
	}
	key := favoriteKey(raw)

	c.mu.Lock()
	for _, e := range c.favorites {
		if favoriteKey(e.SQL) == key {
			existing := e
			c.mu.Unlock()
			return existing, false
		}
	}

	entry := FavoriteEntry{
		Time:     time.Now().UnixMilli(),
		ConnID:   connID,
		Database: strings.TrimSpace(database),
		SQL:      raw,
	}
	if r := []rune(raw); len(r) > favoritesSQLChars {
		entry.SQL = string(r[:favoritesSQLChars]) + " …"
	}
	entry.fillLowerCache()

	c.favSeq++
	entry.ID = c.favSeq
	c.favorites = append(c.favorites, entry)
	if len(c.favorites) > favoritesLimit {
		c.favorites = append([]FavoriteEntry(nil), c.favorites[len(c.favorites)-favoritesLimit:]...)
	}
	c.mu.Unlock()

	c.persistFavorites()
	return entry, true
}

// DeleteFavorite removes one entry by its assigned ID and persists. It reports
// whether an ID was found, so an entry another action already removed answers
// false and the caller can say so.
func (c *Store) DeleteFavorite(id int64) bool {
	c.mu.Lock()
	out := make([]FavoriteEntry, 0, len(c.favorites))
	found := false
	for _, e := range c.favorites {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		c.mu.Unlock()
		return false
	}
	c.favorites = out
	c.mu.Unlock()
	c.persistFavorites()
	return true
}

// ClearFavorites drops every entry and persists, reporting how many went.
func (c *Store) ClearFavorites() int {
	c.mu.Lock()
	n := len(c.favorites)
	c.favorites = nil
	c.mu.Unlock()
	if n > 0 {
		c.persistFavorites()
	}
	return n
}

// persistFavorites rewrites the whole file from memory.
//
// Unlike history this file is not shared with the parent TableProGo client -
// only connections.json and history.json are, which is the only reason
// mergeHistoryLocked exists. So there is no merge here, and a second instance
// of this app running at the same time wins whichever write lands last. That
// is the honest trade for not sharing the file, and it is why the list is
// small and edited one deliberate entry at a time.
func (c *Store) persistFavorites() {
	c.mu.Lock()
	entries := make([]FavoriteEntry, len(c.favorites))
	copy(entries, c.favorites)
	c.mu.Unlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("Failed to encode favorites: %v", err)
		return
	}
	if err := writeFileAtomic(c.favoritesFile, data); err != nil {
		log.Printf("Failed to write favorites: %v", err)
	}
}
