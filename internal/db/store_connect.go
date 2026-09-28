package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/denisenkom/go-mssqldb"
	"github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/sijms/go-ora/v2"
)

// TestConnection opens the DSN and pings; the caller closes the handle.
func TestConnection(cfg ConnConfig) (*sql.DB, error) {
	db, err := createConnection(cfg)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	return db, nil
}

// connect opens and caches a live connection, closing any previous one.
func (c *Store) Connect(cfg ConnConfig) error {
	db, err := TestConnection(cfg)
	if err != nil {
		return err
	}

	c.mu.Lock()
	if old, ok := c.live[cfg.ID]; ok {
		if old.DB != nil {
			old.DB.Close()
		}
		old.closePools()
	}
	c.live[cfg.ID] = &Conn{Config: cfg, DB: db}
	c.mu.Unlock()
	return nil
}

func (c *Store) Disconnect(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.live[id]; ok {
		if conn.DB != nil {
			conn.DB.Close()
			conn.DB = nil
		}
		conn.closePools()
	}
}

// poolFor returns the pool for introspecting the given database. PostgreSQL
// needs its own session per database; the others answer over the main pool.
func (c *Store) poolFor(conn *Conn, database string) (*sql.DB, error) {
	if conn.DB == nil {
		return nil, fmt.Errorf("not connected")
	}
	if conn.Config.Type == DbTypePostgres && database != "" && database != conn.Config.Database {
		return c.perDatabasePool(conn, database)
	}
	return conn.DB, nil
}

// queryPool returns the pool a statement runs on, opening a dedicated session
// when the target database differs for engines that resolve names per session.
func (c *Store) queryPool(conn *Conn, database string) (*sql.DB, error) {
	if database != "" && database != conn.Config.Database {
		switch conn.Config.Type {
		case DbTypeMySQL, DbTypeSQLServer:
			return c.perDatabasePool(conn, database)
		}
	}
	return c.poolFor(conn, database)
}

func (c *Store) perDatabasePool(conn *Conn, database string) (*sql.DB, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if db, ok := conn.pools[database]; ok {
		return db, nil
	}
	cfg := conn.Config
	cfg.Database = database
	db, err := createConnection(cfg)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %q: %w", database, err)
	}
	if conn.pools == nil {
		conn.pools = make(map[string]*sql.DB)
	}
	conn.pools[database] = db
	return db, nil
}

// createConnection opens the pool for the given config. Credentials are never
// spliced into a DSN unescaped: MySQL goes through mysql.Config + OpenDB, the
// URL-style engines through url.UserPassword / BuildUrl, which percent-encode.
// The driver names differ from the engine identifiers for PostgreSQL
// ("postgres") and SQLite ("sqlite3").
func createConnection(cfg ConnConfig) (*sql.DB, error) {
	switch cfg.Type {
	case DbTypeMySQL:
		mc := mysql.NewConfig()
		mc.User = cfg.Username
		mc.Passwd = cfg.Password
		mc.Net = "tcp"
		mc.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
		mc.DBName = cfg.Database
		if cfg.SSLMode == "true" {
			mc.TLSConfig = "true"
		}
		conn, err := mysql.NewConnector(mc)
		if err != nil {
			return nil, err
		}
		return sql.OpenDB(conn), nil
	case DbTypePostgres:
		return sql.Open("postgres", pgURL(cfg))
	case DbTypeSQLite:
		return sql.Open("sqlite3", cfg.Database)
	case DbTypeSQLServer:
		return sql.Open("sqlserver", mssqlURL(cfg))
	case DbTypeOracle:
		conn := go_ora.NewConnector(go_ora.BuildUrl(
			cfg.Host, cfg.Port, cfg.Database, cfg.Username, cfg.Password, nil))
		return sql.OpenDB(conn), nil
	default:
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}
}

// pgURL builds a percent-encoded URL DSN for lib/pq, which unescapes the
// userinfo itself, so passwords with spaces, '@', '/' etc. survive intact.
func pgURL(cfg ConnConfig) string {
	u := url.URL{
		Scheme: "postgres",
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Path:   cfg.Database,
	}
	if cfg.Username != "" || cfg.Password != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}
	q := url.Values{"sslmode": {normalizeSSL(cfg.SSLMode)}}
	u.RawQuery = q.Encode()
	return u.String()
}

// mssqlURL builds the sqlserver:// URL. go-mssqldb's msdsn parser unescapes
// the userinfo, so url.UserPassword keeps special-character credentials whole.
func mssqlURL(cfg ConnConfig) string {
	u := url.URL{
		Scheme: "sqlserver",
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
	}
	if cfg.Username != "" || cfg.Password != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}
	q := url.Values{
		"database":               {cfg.Database},
		"encrypt":                {mssqlEncrypt(cfg.SSLMode)},
		"TrustServerCertificate": {"true"},
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// mssqlEncrypt maps the GUI SSLMode field onto a value go-mssqldb can parse.
// Its msdsn parser only accepts "disable" (case-insensitive) or a boolean
// ("true"/"false"/"1"/"0"). Empty and "require" would fail ParseBool.
func mssqlEncrypt(mode string) string {
	switch strings.ToLower(mode) {
	case "true", "require", "verify-ca", "verify-full", "strict":
		return "true"
	default: // "", "false", "disable", anything unknown
		return "disable"
	}
}

// normalizeSSL maps GUI-friendly values onto PostgreSQL sslmode values.
func normalizeSSL(mode string) string {
	switch mode {
	case "true":
		return "require"
	case "false":
		return "disable"
	case "", "require", "disable", "verify-ca", "verify-full", "prefer":
		return mode
	}
	return "disable"
}
