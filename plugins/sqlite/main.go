// fq-plugin-sqlite: SQLite support for fynequery, delivered as an external
// plugin process speaking the fynequery plugin protocol. Pure Go (no cgo, no
// server); the connection "database" field holds the database file path.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/dbcore/dbcore/sqltext"

	"fynequery/plugins/internal/pluginkit"
	"fynequery/proto"
)

const pluginVersion = "1.0.0"

type sqliteDialect struct{}

func (sqliteDialect) Code() string        { return "sqlite" }
func (sqliteDialect) Name() string        { return "SQLite" }
func (sqliteDialect) Version() string     { return pluginVersion }
func (sqliteDialect) DefaultPort() string { return "" }

func (d sqliteDialect) Open(p proto.ConnectParams) (*sql.DB, string, error) {
	// The file path is carried in the Database field; fall back to Host for
	// DSNs that only fill the host box. ":memory:" opens a scratch database.
	path := strings.TrimSpace(p.Database)
	if path == "" {
		path = strings.TrimSpace(p.Host)
	}
	if path == "" {
		return nil, "", fmt.Errorf("sqlite: no database file specified")
	}
	dsn := path
	if dsn != ":memory:" && !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn + "?mode=rwc" // create the file when absent
	}
	dsn = addSQLitePragmas(dsn)

	handle, version, err := pluginkit.OpenVerify("sqlite", dsn, "SELECT sqlite_version()")
	if err != nil {
		return nil, "", fmt.Errorf("sqlite %s: %w", path, err)
	}
	// `:memory:` is a per-connection scratch database: a pool of >1 would let
	// concurrent statements land on distinct physical handles, each seeing its
	// own empty database (a table created moments earlier would vanish). Pin the
	// pool to one connection, matching the kernel driver and TableProGo.
	if dsn == ":memory:" {
		handle.SetMaxOpenConns(1)
		handle.SetMaxIdleConns(1)
	}
	if version != "" {
		version = "SQLite " + version
	}
	return handle, version, nil
}

// addSQLitePragmas appends the pool-wide pragmas a file database needs, as
// modernc `_pragma` query parameters — the driver runs them on EVERY physical
// connection the pool opens, not just the first one. Without them a file
// database runs the rollback journal (slower, holds exclusive locks longer)
// and busy_timeout=0 (a concurrent writer fails with SQLITE_BUSY immediately
// instead of waiting). :memory: needs none, and a DSN that already carries
// pragmas is left untouched so an advanced user's overrides win.
func addSQLitePragmas(dsn string) string {
	if dsn == ":memory:" || strings.Contains(dsn, "_pragma=") {
		return dsn
	}
	pragmas := []string{
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
	}
	sep := "&"
	if !strings.Contains(dsn, "?") {
		sep = "?"
	}
	// Escape each pragma the way modernc parses them (same shape the
	// dbcore/sqlite kernel DSN builder uses).
	for i, pragma := range pragmas {
		if i == 0 {
			dsn += sep
		} else {
			dsn += "&"
		}
		dsn += "_pragma=" + url.QueryEscape(pragma)
	}
	return dsn
}

func (sqliteDialect) ListTables(db *sql.DB) ([]proto.ObjectInfo, error) {
	rows, err := db.Query(`
		SELECT name, type
		FROM sqlite_master
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proto.ObjectInfo
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		out = append(out, proto.ObjectInfo{Name: name, Kind: kind, Rows: -1})
	}
	return out, rows.Err()
}

func (sqliteDialect) ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error) {
	rows, err := db.Query(
		`SELECT name, type, "notnull", pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proto.ColumnInfo
	for rows.Next() {
		var c proto.ColumnInfo
		var notNull, pk int
		if err := rows.Scan(&c.Name, &c.Type, &notNull, &pk); err != nil {
			return nil, err
		}
		c.Nullable = notNull == 0
		if pk > 0 {
			c.Key = "PK"
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (sqliteDialect) BrowseSQL(table string, limit int) string {
	quoted := strings.ReplaceAll(table, `"`, `""`)
	return fmt.Sprintf("SELECT * FROM \"%s\" LIMIT %d", quoted, limit)
}

// QuoteIdent quotes a SQLite identifier with double quotes (escaping embedded
// quotes). Used by the shared DML builders in pluginkit.
func (sqliteDialect) QuoteIdent(name string) string {
	if q, err := sqltext.QuoteIdent("sqlite", name); err == nil {
		return q
	}
	quoted := strings.ReplaceAll(name, `"`, `""`)
	return `"` + quoted + `"`
}

// Placeholder renders SQLite's positional parameter (?).
func (sqliteDialect) Placeholder(int) string { return "?" }

// Metrics returns a small set of SQLite file-level metrics. SQLite is a
// file-based embedded engine, so it exposes no server-wide counters.
func (sqliteDialect) Metrics(db *sql.DB) ([]proto.Metric, error) {
	const q = `
		SELECT 'Page count', CAST(page_count AS TEXT) FROM pragma_page_count()
		UNION ALL SELECT 'Page size', CAST(page_size AS TEXT) FROM pragma_page_size()
		UNION ALL SELECT 'Freelist pages', CAST(freelist_count AS TEXT) FROM pragma_freelist_count()`
	return pluginkit.KVMetrics(db, "SQLite", q)
}

// CreateDatabase for SQLite creates a new database file at the given path.
// The "name" is treated as a file path; opening with mode=rwc creates the
// file when absent and is a no-op when it already exists (no clobbering).
func (sqliteDialect) CreateDatabase(db *sql.DB, name, _, _ string) error {
	path := strings.TrimSpace(name)
	if path == "" {
		return fmt.Errorf("sqlite: no database file specified")
	}
	dsn := path
	if dsn != ":memory:" && !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn + "?mode=rwc"
	}
	newDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("sqlite create database %q: %w", name, err)
	}
	defer newDB.Close()
	if err := newDB.PingContext(context.Background()); err != nil {
		return fmt.Errorf("sqlite create database %q: %w", name, err)
	}
	return nil
}

func main() { pluginkit.Main(sqliteDialect{}) }
