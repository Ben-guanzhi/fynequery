package db

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The connections and history files are shared with the parent TableProGo
// client, which reads and writes the same paths in the same format. A
// whole-file rewrite from one client used to silently discard whatever the
// other client recorded in between, and a crash mid-write truncated the JSON,
// which then loaded as empty and made the loss permanent on the next save.
// Every writer now merges the on-disk state back in before replacing the
// file, and replaces it through a temp file plus rename.

// writeFileAtomic replaces path with data via a temp file in the same
// directory plus a rename, so an interrupted write cannot leave truncated
// JSON behind. The temp file is created with 0600, which also tightens the
// mode of a file a previous plain WriteFile left looser.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// readJSONFile parses a JSON array stored at path; ok is false when the file
// does not exist. A file that exists but does not parse is moved aside rather
// than deleted, so its bytes stay recoverable instead of being silently
// overwritten by the next save.
func readJSONFile[T any](path string) (entries []T, ok bool, err error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, false, nil
		}
		return nil, false, readErr
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		quarantine := path + ".corrupt"
		if qErr := os.Rename(path, quarantine); qErr == nil {
			log.Printf("%s did not parse (%v); moved it to %s", path, err, quarantine)
		} else {
			log.Printf("%s did not parse (%v); could not move it aside: %v", path, err, qErr)
		}
		return nil, false, err
	}
	return entries, true, nil
}

// connKey identifies a connection by target. Neither the stored id (the
// parent client derives positional ids at read time) nor the sealed password
// (DPAPI seals a fresh random blob on every save) can serve as identity
// across two clients, so the stable core of the definition is the key. A disk
// entry hitting the same target dedupes with the in-memory one winning.
func connKey(c ConnConfig) string {
	return strings.Join([]string{
		c.Type, c.Host, strconv.Itoa(c.Port), c.Username, c.Database, c.SSLMode,
	}, "\x00")
}

// mergeHistoryLocked unions the in-memory log with what another client
// appended to the shared history file since this process last saw it. Entries
// already observed here stay owned by memory (a clear removed them on
// purpose); only genuinely new ones join, and an id both clients minted for
// different statements is renumbered for the newcomer so neither statement is
// lost. Callers hold c.mu.
func (c *Store) mergeHistoryLocked() {
	disk, _, err := readJSONFile[HistoryEntry](c.historyFile)
	if err != nil {
		return
	}
	observed := make(map[int64]bool, len(c.histObserved))
	for _, e := range c.histObserved {
		observed[e.ID] = true
	}
	byID := make(map[int64]HistoryEntry, len(c.history)+len(disk))
	maxID := c.historySeq
	for _, e := range c.history {
		byID[e.ID] = e
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	for _, e := range disk {
		if observed[e.ID] {
			continue
		}
		if old, clash := byID[e.ID]; clash {
			if old == e {
				continue // both clients recorded the same statement under one id
			}
			maxID++
			e.ID = maxID
		}
		byID[e.ID] = e
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	merged := make([]HistoryEntry, 0, len(byID))
	for _, e := range byID {
		merged = append(merged, e)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })
	if len(merged) > historyLimit {
		merged = append([]HistoryEntry(nil), merged[len(merged)-historyLimit:]...)
	}
	c.history = merged
	c.historySeq = maxID
}
