package db

import (
	"database/sql"
	"fmt"
	"sync"

	// MySQL and Oracle are named imports in store_connect.go, which builds
	// their connections through the driver APIs instead of raw DSN strings.
	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

// Supported engine identifiers, matching the parent TablePro project.
const (
	DbTypeMySQL     = "mysql"
	DbTypePostgres  = "postgresql"
	DbTypeSQLite    = "sqlite"
	DbTypeSQLServer = "sqlserver"
	DbTypeOracle    = "oracle"
)

// ConnConfig is a saved connection definition, persisted as connections.json.
type ConnConfig struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	Host           string            `json:"host"`
	Port           int               `json:"port"`
	Username       string            `json:"username"`
	Password       string            `json:"password"`
	Database       string            `json:"database"`
	SSLMode        string            `json:"sslMode"`
	Options        map[string]string `json:"options,omitempty"`
	DatabaseFilter DatabaseFilter    `json:"databaseFilter"`
}

// DatabaseFilter hides databases in the navigator using SQL-LIKE style masks.
type DatabaseFilter struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

// Conn is a live, open connection with lazily opened per-database pools.
//
// The main handle is guarded by the same mutex as the pools, because a reader
// and a disconnect really do run at the same time: the catalog readers used to
// reach for a plain exported field with no lock, so one statement could check it
// as non-nil and then call Query on it after Disconnect had nilled it - a nil
// dereference inside db.Query. Both sides go through handle()/detach() now.
type Conn struct {
	// Config is the snapshot this connection opened with. It is read without a
	// lock everywhere, so it must never be modified in place: an edit lands as a
	// new Conn on the next Connect.
	Config ConnConfig

	mu     sync.Mutex
	db     *sql.DB
	closed bool
	pools  map[string]*sql.DB
}

// handle returns a pool to query. A detached Conn answers with the same error
// every reader has always been expected to show, and the returned handle stays
// safe to use even if a disconnect closes it right after: database/sql reports
// "sql: database is closed" instead of crashing.
func (conn *Conn) handle() (*sql.DB, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.closed || conn.db == nil {
		return nil, fmt.Errorf("not connected")
	}
	return conn.db, nil
}

// isOpen reports whether the connection still has a live handle.
func (conn *Conn) isOpen() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return !conn.closed && conn.db != nil
}

// detach marks the Conn closed and hands back every pool it owned, so the
// caller can close them outside the lock. Closing the pools here would mean
// holding the mutex across a call that waits on running queries, and a reader
// that arrives afterwards is refused by the closed flag: a connection nobody
// keeps a reference to any more can never open a new session behind its back.
func (conn *Conn) detach() []*sql.DB {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.closed = true
	var open []*sql.DB
	if conn.db != nil {
		open = append(open, conn.db)
		conn.db = nil
	}
	for name, db := range conn.pools {
		open = append(open, db)
		delete(conn.pools, name)
	}
	return open
}

// QueryResult is one statement's outcome: either columns+rows (SELECT/SHOW/etc.)
// or an affected/inserted row count (INSERT/UPDATE/DELETE/DDL).
type QueryResult struct {
	Columns       []string
	ColTypes      []string
	Rows          [][]interface{}
	RowCount      int
	AffectedRows  int64
	LastInsertID  int64
	ExecutionTime float64
	Error         string
	// Truncated says the row cap cut the answer short, so a grid full of
	// exactly "limit" rows is not mistaken for the whole story. Only a cap the
	// client injected can truncate: a statement with a window of its own is run
	// as written and stays false however many rows it returns.
	Truncated bool
}

// ColumnInfo describes one column of a table in the schema browser.
type ColumnInfo struct {
	Name         string
	Type         string
	Nullable     bool
	DefaultValue string
	IsPrimaryKey bool
	Comment      string
}

// TableInfo describes one table or view in the schema browser.
type TableInfo struct {
	Name    string
	Schema  string
	Type    string
	Comment string
}
