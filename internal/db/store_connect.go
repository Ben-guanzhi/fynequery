package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/go-sql-driver/mysql"

	"tablepro-fyne/internal/drivercore"
)

// TestConnection opens the engine and pings; the caller closes the handle.
// ctx bounds the handshake: a server that never answers is abandoned when the
// caller cancels (the 停止 button does that) instead of waiting on the OS
// dial forever.
func TestConnection(ctx context.Context, cfg ConnConfig) (drivercore.Engine, error) {
	e, err := openEngine(cfg)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	if err := e.Ping(ctx); err != nil {
		e.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("connection test canceled")
		}
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	return e, nil
}

// connect opens and caches a live connection, closing any previous one.
func (c *Store) Connect(cfg ConnConfig) error {
	return c.ConnectContext(context.Background(), cfg)
}

// ConnectContext is Connect with a caller-owned handshake budget: the UI runs
// it on its background primitive so a dead server surfaces as a cancel, not a
// frozen 连接 button.
func (c *Store) ConnectContext(ctx context.Context, cfg ConnConfig) error {
	e, err := TestConnection(ctx, cfg)
	if err != nil {
		return err
	}

	c.mu.Lock()
	if old, ok := c.live[cfg.ID]; ok {
		for _, e := range old.detach() {
			e.Close()
		}
	}
	c.live[cfg.ID] = &Conn{Config: cfg, engine: e}
	c.mu.Unlock()
	return nil
}

func (c *Store) Disconnect(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.live[id]; ok {
		for _, e := range conn.detach() {
			e.Close()
		}
	}
}

// poolFor returns the pool for introspecting the given database. PostgreSQL
// needs its own session per database; the others answer over the main pool.
//
// ctx belongs here too: opening a per-database session is a dial plus a ping,
// which is the slowest part of reading a remote catalog.
func (c *Store) poolFor(ctx context.Context, conn *Conn, database string) (drivercore.Engine, error) {
	e, err := conn.handle()
	if err != nil {
		return nil, err
	}
	if conn.Config.Type == DbTypePostgres && database != "" && database != conn.Config.Database {
		return c.perDatabasePool(ctx, conn, database)
	}
	return e, nil
}

// queryPool returns the pool a statement runs on, opening a dedicated session
// when the target database differs for engines that resolve names per session.
func (c *Store) queryPool(ctx context.Context, conn *Conn, database string) (drivercore.Engine, error) {
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
func (c *Store) perDatabasePool(ctx context.Context, conn *Conn, database string) (drivercore.Engine, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if e, ok := conn.pools[database]; ok {
		return e, nil
	}
	if conn.closed {
		return nil, fmt.Errorf("not connected")
	}
	cfg := conn.Config
	cfg.Database = database
	e, err := openEngine(cfg)
	if err != nil {
		return nil, err
	}
	if err := e.Ping(ctx); err != nil {
		e.Close()
		return nil, fmt.Errorf("open database %q: %w", database, err)
	}
	if conn.pools == nil {
		conn.pools = make(map[string]drivercore.Engine)
	}
	conn.pools[database] = e
	return e, nil
}

// openEngine returns the Engine a config runs on: an in-process builtin for
// MySQL and SQLite, an out-of-process driver plugin for the rest. This is the
// whole plugin seam - an engine that is neither builtin nor has its plugin
// installed fails here with the install hint from PluginBinary.
func openEngine(cfg ConnConfig) (drivercore.Engine, error) {
	switch cfg.Type {
	case DbTypeMySQL, DbTypeSQLite:
		pool, err := createConnection(cfg)
		if err != nil {
			return nil, err
		}
		return drivercore.NewBuiltin(pool), nil
	case DbTypePostgres, DbTypeSQLServer, DbTypeOracle:
		bin, err := PluginBinary(cfg.Type)
		if err != nil {
			return nil, err
		}
		return drivercore.NewPlugin(bin, cfg)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}
}

// createConnection opens the in-process pool for a builtin engine. Credentials
// are never spliced into a DSN unescaped: MySQL goes through mysql.Config +
// OpenDB, which percent-encodes. The URL-style engines (PostgreSQL, SQL Server,
// Oracle) build their DSN inside their driver plugin - see internal/dsn and the
// cmd/*driver binaries.
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
	case DbTypeSQLite:
		return sql.Open("sqlite3", cfg.Database)
	default:
		return nil, fmt.Errorf("type %s is not a builtin engine (needs a driver plugin)", cfg.Type)
	}
}
