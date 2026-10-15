package db

import (
	"encoding/json"
	"fmt"
	"log"
)

// loadConnections reads and unseals saved connections from disk.
func (c *Store) loadConnections() {
	conns, ok, err := readJSONFile[ConnConfig](c.connFile)
	if err != nil {
		log.Printf("Failed to load connections: %v", err)
		return
	}
	if !ok {
		return
	}
	needsSealing := false
	for i := range conns {
		if conns[i].Password != "" && !isSealedValue(conns[i].Password) {
			needsSealing = true
		}
		pw, err := unprotectSecret(conns[i].Password)
		if err != nil {
			log.Printf("Connection %s: %v", conns[i].ID, err)
			pw = ""
		}
		conns[i].Password = pw
	}
	c.saved = conns
	c.connObserved = append([]ConnConfig(nil), conns...)
	if needsSealing {
		c.persist()
	}
	log.Printf("Loaded %d connections from %s", len(conns), c.connFile)
}

// persist seals passwords and writes saved connections to disk.
func (c *Store) persist() {
	if c.connFile == "" {
		return // no state directory configured (tests build bare Stores)
	}
	c.mu.Lock()
	sealed := make([]ConnConfig, len(c.saved))
	copy(sealed, c.saved)
	observed := make(map[string]bool, len(c.connObserved))
	for _, cfg := range c.connObserved {
		observed[connKey(cfg)] = true
	}
	c.mu.Unlock()

	for i := range sealed {
		pw, err := protectSecret(sealed[i].Password)
		if err != nil {
			log.Printf("Failed to encrypt password for %s: %v", sealed[i].ID, err)
			return
		}
		sealed[i].Password = pw
	}

	// The parent client shares this file. Fold in the connections it saved
	// since we last saw the file, so one client adding a connection no longer
	// erases the other's. Entries this process already observed are owned by
	// memory: their absence here is this save's deletion, not someone else's
	// write, so they are not resurrected.
	var adopt []ConnConfig
	disk, diskOK, diskErr := readJSONFile[ConnConfig](c.connFile)
	if diskErr != nil {
		log.Printf("Failed to load connections for merging: %v", diskErr)
	}
	if diskOK {
		known := make(map[string]bool, len(sealed))
		for _, cfg := range sealed {
			known[connKey(cfg)] = true
		}
		for _, cfg := range disk {
			key := connKey(cfg)
			if known[key] || observed[key] {
				continue
			}
			known[key] = true
			sealed = append(sealed, cfg)
			// Adopt the connection into memory too, so the next save treats
			// it as this process's own instead of dropping it again. As at
			// load time, a password that cannot be opened here (sealed on
			// another machine or account) is left empty for the user to retype.
			if pw, err := unprotectSecret(cfg.Password); err == nil {
				cfg.Password = pw
			} else {
				log.Printf("Connection %s: %v", cfg.ID, err)
				cfg.Password = ""
			}
			adopt = append(adopt, cfg)
		}
	}

	data, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		log.Printf("Failed to serialize connections: %v", err)
		return
	}
	if err := writeFileAtomic(c.connFile, data); err != nil {
		log.Printf("Failed to write connections file: %v", err)
		return
	}
	// The keyring entries the previous file pointed at that this save dropped
	// (deleted connection, changed or cleared password) are now orphans.
	if diskOK {
		secretsGC(passwordsOf(disk), passwordsOf(sealed))
	}
	// The temp file behind writeFileAtomic is created 0600, which also
	// tightens the mode of a connections.json another tool left world-readable
	// - the one protection a plain-text password has on non-Windows builds.
	c.mu.Lock()
	c.saved = append(c.saved, adopt...)
	c.connObserved = append([]ConnConfig(nil), sealed...)
	c.mu.Unlock()
}

// nextConnSeq returns one past the highest "c<n>" id in use. Seeding from
// len(saved) instead would recycle ids after deletions: c0,c1,c2 minus c0
// makes the next add produce "c2" again, colliding with the survivor.
func nextConnSeq(conns []ConnConfig) int {
	max := -1
	for _, cfg := range conns {
		var n int
		if _, err := fmt.Sscanf(cfg.ID, "c%d", &n); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}

func (c *Store) AddConnection(cfg ConnConfig) {
	c.mu.Lock()
	cfg.ID = fmt.Sprintf("c%d", c.idSeq)
	c.idSeq++
	c.saved = append(c.saved, cfg)
	c.mu.Unlock()
	c.persist()
}

func (c *Store) UpdateConnection(cfg ConnConfig) error {
	c.mu.Lock()
	for i, old := range c.saved {
		if old.ID == cfg.ID {
			if cfg.Password == "" {
				cfg.Password = old.Password
			}
			c.saved[i] = cfg
			c.mu.Unlock()
			c.persist()
			return nil
		}
	}
	c.mu.Unlock()
	return fmt.Errorf("connection not found: %s", cfg.ID)
}

func (c *Store) DeleteConnection(id string) error {
	c.mu.Lock()
	for i, cfg := range c.saved {
		if cfg.ID == id {
			if conn, ok := c.live[id]; ok {
				// Detach first, then close: the flag is what stops a query that
				// still holds this *Conn from hanging a new session on it after
				// the map entry is gone.
				for _, db := range conn.detach() {
					db.Close()
				}
				delete(c.live, id)
			}
			c.saved = append(c.saved[:i], c.saved[i+1:]...)
			c.mu.Unlock()
			c.persist()
			return nil
		}
	}
	c.mu.Unlock()
	return fmt.Errorf("connection not found: %s", id)
}

func (c *Store) FindConnection(id string) (ConnConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cfg := range c.saved {
		if cfg.ID == id {
			return cfg, true
		}
	}
	return ConnConfig{}, false
}

func (c *Store) ConnectionStatus(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, ok := c.live[id]
	if !ok {
		return false
	}
	return conn.isOpen()
}

func (c *Store) LiveConfig(id string) (ConnConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, ok := c.live[id]
	if !ok {
		return ConnConfig{}, false
	}
	return conn.Config, true
}
