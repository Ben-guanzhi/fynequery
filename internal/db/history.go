package db

import (
	"encoding/json"
	"log"
	"os"
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
}

// historyLimit keeps the on-disk log bounded.
const historyLimit = 500

// historySQLChars truncates stored statements so one huge paste can't bloat the file.
const historySQLChars = 4000

// loadHistory reads the persisted statement log.
func (c *Store) loadHistory() {
	data, err := os.ReadFile(c.historyFile)
	if err != nil {
		return
	}
	var entries []HistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Printf("Failed to load history: %v", err)
		return
	}
	c.history = entries
	for _, e := range entries {
		if e.ID > c.historySeq {
			c.historySeq = e.ID
		}
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

	c.mu.Lock()
	c.historySeq++
	entry.ID = c.historySeq
	c.history = append(c.history, entry)
	if len(c.history) > historyLimit {
		c.history = append([]HistoryEntry(nil), c.history[len(c.history)-historyLimit:]...)
	}
	c.mu.Unlock()

	c.PersistHistory()
}

func (c *Store) PersistHistory() {
	c.mu.Lock()
	entries := make([]HistoryEntry, len(c.history))
	copy(entries, c.history)
	c.mu.Unlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("Failed to encode history: %v", err)
		return
	}
	if err := os.WriteFile(c.historyFile, data, 0600); err != nil {
		log.Printf("Failed to write history: %v", err)
	}
}

// clearHistory removes all recorded statements and persists the empty log.
func (c *Store) ClearHistory() int {
	c.mu.Lock()
	n := len(c.history)
	c.history = nil
	c.mu.Unlock()
	c.PersistHistory()
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
