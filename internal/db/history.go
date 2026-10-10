package db

import (
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"
)

// HistoryEntry records one executed statement, mirroring the parent project.
type HistoryEntry struct {
	ID            int64   `json:"id"`
	Time          int64   `json:"time"`
	ConnID        string  `json:"connId"`
	Database      string  `json:"database"`
	Table         string  `json:"table,omitempty"`
	SQL           string  `json:"sql"`
	RowCount      int     `json:"rowCount"`
	Affected      int64   `json:"affectedRows"`
	ExecutionTime float64 `json:"executionTime"`
	Error         string  `json:"error,omitempty"`

	// 过滤用小写缓存（json:"-" 不入盘）：加载/追加时各 ToLower 一次，
	// 历史框每击键的过滤从「3×ToLower + 3×Contains 每记录」降为「3×Contains」（SICP ch3 记忆化）。
	lowerSQL, lowerTable, lowerDatabase string
}

// fillLowerCache 填充过滤用小写缓存；每条记录只在加载/追加时调用一次。
func (e *HistoryEntry) fillLowerCache() {
	e.lowerSQL = strings.ToLower(e.SQL)
	e.lowerTable = strings.ToLower(e.Table)
	e.lowerDatabase = strings.ToLower(e.Database)
}

// MatchesFilter reports whether the entry matches the (already-lowercased)
// filter across SQL/Table/Database, using the precomputed lowercase cache.
// 未匹配的 ToLower 分配被摊到加载/追加一次（SICP ch3 记忆化）。
func (e *HistoryEntry) MatchesFilter(lowerFilter string) bool {
	return strings.Contains(e.lowerSQL, lowerFilter) ||
		strings.Contains(e.lowerTable, lowerFilter) ||
		strings.Contains(e.lowerDatabase, lowerFilter)
}

// historyLimit keeps the on-disk log bounded.
const historyLimit = 500

// historySQLChars truncates stored statements so one huge paste can't bloat the file.
const historySQLChars = 4000

// loadHistory reads the persisted statement log.
func (c *Store) loadHistory() {
	entries, ok, err := readJSONFile[HistoryEntry](c.historyFile)
	if err != nil {
		log.Printf("Failed to load history: %v", err)
		return
	}
	if !ok {
		return
	}
	c.history = entries
	c.histObserved = append([]HistoryEntry(nil), entries...)
	for i := range entries {
		e := &entries[i]
		if e.ID > c.historySeq {
			c.historySeq = e.ID
		}
		e.fillLowerCache()
	}
	log.Printf("Loaded %d history entries from %s", len(entries), c.historyFile)
}

// recordStatement appends one executed statement and persists the log.
func (c *Store) RecordStatement(connID, database, query string, res QueryResult) {
	sql := strings.TrimSpace(query)
	if sql == "" {
		return
	}
	// Cut on runes so a multibyte statement cannot end mid-byte.
	if r := []rune(sql); len(r) > historySQLChars {
		sql = string(r[:historySQLChars]) + " …"
	}

	entry := HistoryEntry{
		Time:          time.Now().UnixMilli(),
		ConnID:        connID,
		Database:      strings.TrimSpace(database),
		Table:         statementTable(sql),
		SQL:           sql,
		Error:         res.Error,
		RowCount:      res.RowCount,
		Affected:      res.AffectedRows,
		ExecutionTime: res.ExecutionTime,
	}
	entry.fillLowerCache()

	c.mu.Lock()
	c.historySeq++
	entry.ID = c.historySeq
	c.history = append(c.history, entry)
	if len(c.history) > historyLimit {
		c.history = append([]HistoryEntry(nil), c.history[len(c.history)-historyLimit:]...)
	}
	c.mu.Unlock()

	c.persistHistorySoon()
}

// historyPersistDelay bounds how often the whole statement log is rewritten.
// Every executed statement used to trigger a full rewrite of up to 500 entries,
// which is a lot of identical writes for a log the user reads occasionally: a
// burst now writes once and the tail goes out when the window closes. The cost
// is stated honestly - a process killed before that flush loses at most the
// statements recorded inside one delay.
const historyPersistDelay = 5 * time.Second

// persistHistorySoon records that the log changed and writes it unless one
// delay has already passed since the last write.
func (c *Store) persistHistorySoon() {
	c.mu.Lock()
	c.historyDirty = true
	recent := !c.historyFlushed.IsZero() && time.Since(c.historyFlushed) < historyPersistDelay
	c.mu.Unlock()
	if !recent {
		c.PersistHistory()
	}
}

// PersistHistory writes the statement log now; nothing is rewritten when no
// entry has changed since the last write. Quitting the app calls it so the
// throttled tail of a session is never left behind. Statements another client
// recorded since the last write are folded in first, so neither side's log
// loses entries.
func (c *Store) PersistHistory() {
	c.persistHistoryNow(true)
}

// persistHistoryNow writes the statement log. mergeDisk controls whether what
// other clients appended to the shared file since the last write is folded in
// first: a record wants that, a full clear does not - wiping the log is a
// statement about every entry, wherever it was written.
func (c *Store) persistHistoryNow(mergeDisk bool) {
	c.mu.Lock()
	if !c.historyDirty || c.historyFile == "" {
		c.mu.Unlock()
		return
	}
	if mergeDisk {
		c.mergeHistoryLocked()
	}
	entries := make([]HistoryEntry, len(c.history))
	copy(entries, c.history)
	c.historyDirty = false
	c.historyFlushed = time.Now()
	c.mu.Unlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("Failed to encode history: %v", err)
		return
	}
	if err := writeFileAtomic(c.historyFile, data); err != nil {
		log.Printf("Failed to write history: %v", err)
		return
	}
	c.mu.Lock()
	c.histObserved = append([]HistoryEntry(nil), entries...)
	c.mu.Unlock()
}

// clearHistory removes all recorded statements and persists the empty log.
func (c *Store) ClearHistory() int {
	c.mu.Lock()
	n := len(c.history)
	c.history = nil
	c.historyDirty = true
	c.mu.Unlock()
	c.persistHistoryNow(false)
	return n
}

// sqlCommentRe strips comments and string literals so a keyword inside them
// cannot be mistaken for a table reference.
var sqlCommentRe = regexp.MustCompile(`(--[^\n]*)|(/\*.*?\*/)|('(?:[^']|'')*')`)

// queryTableRe grabs the object reference after FROM/INTO/UPDATE/JOIN,
// including multi-part names whose parts are quoted ("db"."t", [db].[t]).
var queryTableRe = regexp.MustCompile("(?i)\\b(?:from|into|update|join)\\s+((?:\"[^\"]+\"|\\[[^\\]]+\\]|\x60[^\x60]+\x60|[\\w.]+)(?:\\.(?:\"[^\"]+\"|\\[[^\\]]+\\]|\x60[^\x60]+\x60|[\\w.]+))*)")

// statementTable extracts the primary table a statement touches, if any.
func statementTable(query string) string {
	m := queryTableRe.FindStringSubmatch(sqlCommentRe.ReplaceAllString(query, " "))
	if m == nil {
		return ""
	}
	ref := strings.Trim(m[1], "\"`[]")
	parts := strings.Split(ref, ".")
	table := strings.Trim(parts[len(parts)-1], "\"`[]")
	if !isIdentifierLike(table) {
		return ""
	}
	return strings.ToLower(table)
}

// isIdentifierLike reports whether name looks like a bare identifier.
func isIdentifierLike(name string) bool {
	for i, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return name != ""
}
