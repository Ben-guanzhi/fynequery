package drivercore

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// builtinEngine runs SQL in-process against a *sql.DB the caller opened. It is
// what MySQL and SQLite use; the other engines run as out-of-process plugins.
type builtinEngine struct {
	db *sql.DB
}

// NewBuiltin wraps an already-open pool.
func NewBuiltin(db *sql.DB) Engine {
	return &builtinEngine{db: db}
}

// UnderlyingDB returns the *sql.DB behind a builtin engine, or nil for a plugin
// engine (which has no in-process pool). It exists for tests that need to reach
// driver-specific knobs like SetMaxOpenConns.
func UnderlyingDB(e Engine) *sql.DB {
	if b, ok := e.(*builtinEngine); ok {
		return b.db
	}
	return nil
}

func (e *builtinEngine) Query(ctx context.Context, query string, args ...interface{}) (RowSet, error) {
	rows, err := e.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlRowSet{rows}, nil
}

func (e *builtinEngine) Exec(ctx context.Context, query string, args ...interface{}) (ExecResult, error) {
	res, err := e.db.ExecContext(ctx, query, args...)
	if err != nil {
		return ExecResult{}, err
	}
	affected, _ := res.RowsAffected()
	lastID, _ := res.LastInsertId()
	return ExecResult{Affected: affected, LastInsertID: lastID}, nil
}

func (e *builtinEngine) Begin(ctx context.Context) (Tx, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &builtinTx{tx: tx}, nil
}

func (e *builtinEngine) Ping(ctx context.Context) error { return e.db.PingContext(ctx) }
func (e *builtinEngine) Close() error                   { return e.db.Close() }

type builtinTx struct {
	tx *sql.Tx
}

func (t *builtinTx) Exec(ctx context.Context, query string, args ...interface{}) (int64, error) {
	res, err := t.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

func (t *builtinTx) Commit(ctx context.Context) error   { return t.tx.Commit() }
func (t *builtinTx) Rollback(ctx context.Context) error { return t.tx.Rollback() }

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "yes", "y":
		return true, nil
	case "", "0", "f", "false", "no", "n":
		return false, nil
	}
	return false, fmt.Errorf("cannot read %q as bool", s)
}

func parseInt(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}
