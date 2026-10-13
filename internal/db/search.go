package db

import (
	"github.com/Ben-guanzhi/TableProGo/tableprosql"

	"fmt"
	"sort"
	"strings"
)

// Ported from the parent TableProGo project's search.go: the catalog half of
// the quick-open box.

// ObjectMatch is one catalog hit for the quick-open box: enough to address the
// object the way the rest of the app does, plus what it is.
type ObjectMatch struct {
	ConnID   string
	Database string
	Schema   string
	Table    string
	Type     string
}

// A keystroke triggers a search, so the walk stops well before it can become a
// full catalog dump.
const (
	SearchDefaultLimit = 50
	SearchMaxLimit     = 200
	searchDatabaseCap  = 32
	// Enough of the reasons to explain an empty result without turning one
	// keystroke into a wall of text when a whole server is unreachable.
	searchFailureCap = 3
)

// searchObjects matches a name fragment against tables and views across every
// live connection, which is what lets the quick-open box find a table that is
// not open anywhere yet.
//
// A failure in one database is not reported as long as another one answered,
// because a partial list is more useful than an error. When nothing came back
// and something did fail, that failure is returned: "no matches" and "the
// catalog could not be read" look the same in the UI but are not the same
// thing.
func (c *Store) SearchObjects(pattern string, limit int) ([]ObjectMatch, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return []ObjectMatch{}, nil
	}
	if limit <= 0 || limit > SearchMaxLimit {
		limit = SearchDefaultLimit
	}

	c.mu.Lock()
	conns := make([]*Conn, 0, len(c.live))
	for _, conn := range c.live {
		if conn.DB != nil {
			conns = append(conns, conn)
		}
	}
	c.mu.Unlock()
	// Connection ids are index-based, so sorting them keeps results stable
	// between keystrokes instead of reshuffling with the map.
	sort.Slice(conns, func(i, j int) bool { return conns[i].Config.ID < conns[j].Config.ID })

	found := []ObjectMatch{}
	var failures []string
	record := func(scope string, err error) {
		if err == nil {
			return
		}
		if len(failures) < searchFailureCap {
			failures = append(failures, scope+": "+err.Error())
		}
	}
	for _, conn := range conns {
		if len(found) >= limit {
			break
		}
		databases, err := c.Databases(conn.Config.ID)
		if err != nil {
			record(conn.Config.Name, err)
			continue
		}
		sort.Strings(databases)
		if len(databases) > searchDatabaseCap {
			databases = databases[:searchDatabaseCap]
		}
		for _, database := range databases {
			if len(found) >= limit {
				break
			}
			hits, err := c.SearchInDatabase(conn, database, pattern, limit-len(found))
			if err != nil {
				// One database being unreachable - offline, no rights - must not
				// hide the matches the others have.
				record(conn.Config.Name+"."+database, err)
				continue
			}
			found = append(found, hits...)
		}
	}
	if len(found) == 0 && len(failures) > 0 {
		return found, fmt.Errorf("catalog search failed: %s", strings.Join(failures, "; "))
	}
	return found, nil
}

// likePattern wraps the fragment for a LIKE test, escaping the wildcards the
// user typed so "a_b" cannot stand for any three characters.
func likePattern(fragment string) string {
	return tableprosql.LikePattern(fragment)
}

func (c *Store) SearchInDatabase(conn *Conn, database, fragment string, limit int) ([]ObjectMatch, error) {
	if limit <= 0 {
		return nil, nil
	}
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		return nil, err
	}
	return d.search(c, conn, database, fragment, limit)
}
