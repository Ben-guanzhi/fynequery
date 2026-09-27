package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

func connFilePath() string {
	return stateFilePath("connections.json")
}

// loadConnections reads and unseals saved connections from disk.
func (c *Client) loadConnections() {
	data, err := os.ReadFile(c.connFile)
	if err != nil {
		return
	}
	var conns []ConnConfig
	if err := json.Unmarshal(data, &conns); err != nil {
		log.Printf("Failed to load connections: %v", err)
		return
	}
	needsSealing := false
	for i := range conns {
		if conns[i].Password != "" && !strings.HasPrefix(conns[i].Password, secretPrefix) {
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
	if needsSealing {
		c.persist()
	}
	log.Printf("Loaded %d connections from %s", len(conns), c.connFile)
}

// persist seals passwords and writes saved connections to disk.
func (c *Client) persist() {
	sealed := make([]ConnConfig, len(c.saved))
	copy(sealed, c.saved)
	for i := range sealed {
		pw, err := protectSecret(sealed[i].Password)
		if err != nil {
			log.Printf("Failed to encrypt password for %s: %v", sealed[i].ID, err)
			return
		}
		sealed[i].Password = pw
	}
	data, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		log.Printf("Failed to serialize connections: %v", err)
		return
	}
	if err := os.WriteFile(c.connFile, data, 0600); err != nil {
		log.Printf("Failed to write connections file: %v", err)
	}
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

func (c *Client) addConnection(cfg ConnConfig) {
	c.mu.Lock()
	cfg.ID = fmt.Sprintf("c%d", c.idSeq)
	c.idSeq++
	c.saved = append(c.saved, cfg)
	c.mu.Unlock()
	c.persist()
}

func (c *Client) updateConnection(cfg ConnConfig) error {
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

func (c *Client) deleteConnection(id string) error {
	c.mu.Lock()
	for i, cfg := range c.saved {
		if cfg.ID == id {
			if conn, ok := c.live[id]; ok {
				if conn.DB != nil {
					conn.DB.Close()
				}
				conn.closePools()
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

func (c *Client) findConnection(id string) (ConnConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cfg := range c.saved {
		if cfg.ID == id {
			return cfg, true
		}
	}
	return ConnConfig{}, false
}

func (c *Client) connectionStatus(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, ok := c.live[id]
	return ok && conn.DB != nil
}

func (c *Client) liveConfig(id string) (ConnConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, ok := c.live[id]
	if !ok {
		return ConnConfig{}, false
	}
	return conn.Config, true
}