// Package db is the data layer of fynequery: a registry of drivers where
// MySQL is built in and every other database type is served by an external
// plugin process (see pluginhost.go). The UI talks to Sessions only and
// never to a concrete driver.
package db

import (
	"errors"
	"sort"
	"sync"

	"fynequery/proto"
)

// ConnectParams is the dial parameter set shared with plugins.
type ConnectParams = proto.ConnectParams

// DriverInfo describes one registered driver.
type DriverInfo struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	DefaultPort string `json:"defaultPort"`
	Plugin      bool   `json:"plugin"` // served by an external plugin process
}

// Dialer opens sessions for one database type.
type Dialer interface {
	Info() DriverInfo
	Connect(p proto.ConnectParams) (Session, error)
}

// Session is one live connection to a database.
type Session interface {
	// Server returns a server identification string (shown in the status
	// area), empty when unknown.
	Server() string
	Tables() ([]proto.ObjectInfo, error)
	Columns(table string) ([]proto.ColumnInfo, error)
	Query(sqlText string, maxRows int) (proto.QueryResult, error)
	// Browse returns the first rows of a table (dialect-specific paging).
	Browse(table string, limit int) (proto.QueryResult, error)
	Close()
}

// Updater is optionally implemented by Session types that support row-level
// DML, enabling the editable data grid. Values are passed as *string (nil =
// SQL NULL); pk carries the primary-key columns that identify the target row
// and must be non-empty. Sessions that do not implement Updater leave the
// grid read-only.
//
// Sessions that also implement BatchUpdater apply a whole commit atomically
// (single transaction with rollback); otherwise the UI falls back to calling
// UpdateRow/DeleteRow one statement at a time.
type Updater interface {
	UpdateRow(table string, pk, changes map[string]*string) error
	DeleteRow(table string, pk map[string]*string) error
	InsertRow(table string, values map[string]*string) error
}

// RowEdit pairs a primary key with the changed columns for a row update.
type RowEdit struct {
	PK      map[string]*string
	Changes map[string]*string
}

// BatchUpdater is optionally implemented by Updater sessions that can apply a
// whole commit inside one transaction. ddl is run as part of the same
// transaction (MySQL's implicit-commit statements like DDL/DCL still force
// the transaction to commit server-side, so they are best-effort but kept in
// order). Returns on the first error and rolls back so no partial change is
// persisted.
type BatchUpdater interface {
	ApplyChanges(table string, updates []RowEdit, deletes []map[string]*string, inserts []map[string]*string, ddl []string) error
}

// DatabaseCreator is optionally implemented by Session types that can create a
// new database on the server. It backs the in-app "new database" flow (the
// sidebar "+" and the open-database sheet's "新建..." button). charset/collation
// are passed as opts (e.g. {"charset":"utf8mb4"}); implementors must validate
// and quote them to avoid injection.
type DatabaseCreator interface {
	CreateDatabase(name string, opts map[string]string) error
}

// CanCreateDatabase reports whether a session can create databases.
func CanCreateDatabase(s Session) bool {
	_, ok := s.(DatabaseCreator)
	return ok
}

// Canceller is optionally implemented by Session types that can terminate
// the statement currently running on the session (the editor's Cancel
// button, TablePlus process-list "Kill QUERY"). The builtin MySQL driver
// implements it via KILL QUERY on a separate connection.
type Canceller interface {
	KillQuery() error
}

// Transactional is optionally implemented by Session types that support a
// manual transaction (TablePlus "Begin Transaction" mode). Begin starts a
// transaction; subsequent Query/Browse/DML run inside it until Commit or
// Rollback. InTransaction reports the current state so the UI can show the
// right controls and auto-rollback a transaction left open on disconnect.
type Transactional interface {
	Begin() error
	Commit() error
	Rollback() error
	InTransaction() bool
}

// Streamer is optionally implemented by Session types that can fetch a
// statement's result incrementally, emitting batches of up to `batch` rows as
// they are read from the server. This keeps memory bounded and lets the grid
// paint a large result progressively instead of blocking on one giant result
// set. The built-in MySQL driver streams from the cursor; plugin connections
// stream over a multi-frame protocol extension. emit receives the column
// metadata on the first call only (cols is nil afterwards); returning
// (false, err) stops the stream early. The returned QueryResult carries the
// aggregate rows affected (non-SELECT) and duration.
type Streamer interface {
	QueryStream(sqlText string, batch int, emit func(cols []proto.ColumnInfo, rows [][]*string) (bool, error)) (proto.QueryResult, error)
}

// Metricser is optionally implemented by Session types that can report a
// curated set of read-only server metrics (connections, uptime, cache hit
// ratio, ...). It backs the in-app server-metrics panel; sessions without it
// simply report no metrics.
type Metricser interface {
	Metrics() ([]proto.Metric, error)
}

// ErrQueryCancelled is returned by Query implementations whose in-flight
// statement was terminated by KillQuery (as opposed to a server error).
var ErrQueryCancelled = errors.New("query cancelled")

var (
	regMu   sync.RWMutex
	drivers = map[string]Dialer{}
)

// Register adds (or replaces) a driver. Called at startup for the built-in
// MySQL driver and for every discovered plugin.
func Register(d Dialer) {
	regMu.Lock()
	defer regMu.Unlock()
	drivers[d.Info().Code] = d
}

// Driver looks a driver up by code ("mysql", "postgres", ...).
func Driver(code string) (Dialer, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	d, ok := drivers[code]
	return d, ok
}

// Drivers lists all registered drivers sorted by display name.
func Drivers() []DriverInfo {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]DriverInfo, 0, len(drivers))
	for _, d := range drivers {
		out = append(out, d.Info())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// DefaultPort reports the conventional port for a driver code ("" unknown).
func DefaultPort(code string) string {
	if d, ok := Driver(code); ok {
		return d.Info().DefaultPort
	}
	return ""
}
