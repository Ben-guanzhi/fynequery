package db

import (
	"fmt"
	"sync"

	// SQLite runs in-process (see createConnection); MySQL's driver is a named
	// import in store_connect.go. PostgreSQL, SQL Server and Oracle run as
	// out-of-process driver plugins (see openEngine / drivercore), so their
	// drivers are not imported here.
	_ "github.com/mattn/go-sqlite3"

	"tablepro-fyne/internal/drivercore"
)

// ConnConfig and DatabaseFilter live in drivercore so a driver plugin can build
// its own DSN without importing this package; they are aliased here so the rest
// of the app keeps spelling them db.ConnConfig.
type ConnConfig = drivercore.ConnConfig
type DatabaseFilter = drivercore.DatabaseFilter

// Supported engine identifiers, matching the parent TablePro project.
const (
	DbTypeMySQL     = "mysql"
	DbTypePostgres  = "postgresql"
	DbTypeSQLite    = "sqlite"
	DbTypeSQLServer = "sqlserver"
	DbTypeOracle    = "oracle"
)

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
	engine drivercore.Engine
	closed bool
	pools  map[string]drivercore.Engine
}

// handle returns a pool to query. A detached Conn answers with the same error
// every reader has always been expected to show, and the returned handle stays
// safe to use even if a disconnect closes it right after: the Engine reports
// "driver plugin is not running" (plugin) or "sql: database is closed" (builtin)
// instead of crashing.
func (conn *Conn) handle() (drivercore.Engine, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.closed || conn.engine == nil {
		return nil, fmt.Errorf("not connected")
	}
	return conn.engine, nil
}

// isOpen reports whether the connection still has a live handle.
func (conn *Conn) isOpen() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return !conn.closed && conn.engine != nil
}

// detach marks the Conn closed and hands back every pool it owned, so the
// caller can close them outside the lock. Closing the pools here would mean
// holding the mutex across a call that waits on running queries, and a reader
// that arrives afterwards is refused by the closed flag: a connection nobody
// keeps a reference to any more can never open a new session behind its back.
func (conn *Conn) detach() []drivercore.Engine {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.closed = true
	var open []drivercore.Engine
	if conn.engine != nil {
		open = append(open, conn.engine)
		conn.engine = nil
	}
	for name, e := range conn.pools {
		open = append(open, e)
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
