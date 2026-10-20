package db

import (
	"context"
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
// ctx bounds the handshake: a server that never answers is abandoned when the
// caller cancels (the 停止 button does that) instead of waiting on the OS
// dial forever.
func TestConnection(ctx context.Context, cfg ConnConfig) (*sql.DB, error) {
	db, err := createConnection(cfg)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("connection test canceled")
		}
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	return db, nil
}

// connect opens and caches a live connection, closing any previous one.
func (c *Store) Connect(cfg ConnConfig) error {
	return c.ConnectContext(context.Background(), cfg)
}

// ConnectContext is Connect with a caller-owned handshake budget: the UI runs
// it on its background primitive so a dead server surfaces as a cancel, not a
// frozen 连接 button.
func (c *Store) ConnectContext(ctx context.Context, cfg ConnConfig) error {
	db, err := TestConnection(ctx, cfg)
	if err != nil {
		return err
	}

	c.mu.Lock()
	if old, ok := c.live[cfg.ID]; ok {
		for _, db := range old.detach() {
			db.Close()
		}
	}
	c.live[cfg.ID] = &Conn{Config: cfg, db: db}
	c.mu.Unlock()
	return nil
}

func (c *Store) Disconnect(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.live[id]; ok {
		for _, db := range conn.detach() {
			db.Close()
		}
	}
}

// poolFor returns the pool for introspecting the given database. PostgreSQL
// needs its own session per database; the others answer over the main pool.
//
// ctx belongs here too: opening a per-database session is a dial plus a ping,
// which is the slowest part of reading a remote catalog.
func (c *Store) poolFor(ctx context.Context, conn *Conn, database string) (*sql.DB, error) {
	db, err := conn.handle()
	if err != nil {
		return nil, err
	}
	if conn.Config.Type == DbTypePostgres && database != "" && database != conn.Config.Database {
		return c.perDatabasePool(ctx, conn, database)
	}
	return db, nil
}

// queryPool returns the pool a statement runs on, opening a dedicated session
// when the target database differs for engines that resolve names per session.
func (c *Store) queryPool(ctx context.Context, conn *Conn, database string) (*sql.DB, error) {
	if database != "" && database != conn.Config.Database {
		switch conn.Config.Type {
		case DbTypeMySQL, DbTypeSQLServer:
			return c.perDatabasePool(ctx, conn, database)
		}
	}
	return c.poolFor(ctx, conn, database)
}

// perDatabasePool opens, or returns the already open, session for one database.
// The closed check is inside the same critical section that registers the pool,
// so a Conn that has been detached - by Disconnect, or by DeleteConnection while
// a query still held it - cannot grow a new session nobody will ever close.
func (c *Store) perDatabasePool(ctx context.Context, conn *Conn, database string) (*sql.DB, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if db, ok := conn.pools[database]; ok {
		return db, nil
	}
	if conn.closed {
		return nil, fmt.Errorf("not connected")
	}
	cfg := conn.Config
	cfg.Database = database
	db, err := createConnection(cfg)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
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
		// UPDATE can report rows MATCHED or rows whose bytes changed, and the
		// write path's "matched no row, so nothing was committed" guard
		// (changes.go) has to mean the same thing on every engine: on the driver
		// default, re-saving a value that is already stored reports 0 and aborts
		// a batch that did nothing wrong. SQLite is measured to answer with the
		// matched row (changes_apply_test.go); PostgreSQL and MySQL are answered
		// by the write half of contract_test.go, because a server this test suite
		// is not dialling cannot be asserted about from here.
		mc.ClientFoundRows = true
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
		"TrustServerCertificate": {mssqlTrustServerCertificate(cfg.SSLMode)},
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// mssqlEncrypt maps the GUI 加密 choice onto a value go-mssqldb can parse. Its
// msdsn parser accepts only "disable" or a boolean - "require", "strict" and
// the empty string all fail ParseBool, which is what TestMssqlDSNReachesTheDialer
// guards against.
//
// Encryption is the default: an unset or unrecognised mode encrypts, and
// turning it off is a choice that has to be named ("false"/"disable"). That is
// also the one-time migration for connections saved while the old mapping was
// in force - their empty mode now means encrypt-and-verify, so a stored
// connection silently travelling in cleartext is no longer possible.
func mssqlEncrypt(mode string) string {
	switch strings.ToLower(mode) {
	case "false", "disable":
		return "disable"
	default: // "", "true", "trust", anything else
		return "true"
	}
}

// mssqlTrustServerCertificate is the verification knob this driver really
// offers, and it defaults to verifying: only the mode named "trust" skips the
// certificate chain, which is the choice for a server with a self-signed
// certificate.
func mssqlTrustServerCertificate(mode string) string {
	if strings.ToLower(mode) == "trust" {
		return "true"
	}
	return "false"
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
