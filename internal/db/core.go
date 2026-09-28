package db

import (
	"database/sql"
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

// secretPrefix marks a password sealed with DPAPI on Windows. Values without
// it are legacy plain text and pass through, matching the parent project.
const secretPrefix = "dpapi:"

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
type Conn struct {
	Config ConnConfig
	DB     *sql.DB

	mu    sync.Mutex
	pools map[string]*sql.DB
}

func (conn *Conn) closePools() {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for name, db := range conn.pools {
		db.Close()
		delete(conn.pools, name)
	}
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
