package db

// Built-in MySQL driver (default database type, compiled into the app).
// MariaDB servers speak the same protocol and work through this driver too.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	"fynequery/internal/sqlkit"
	"fynequery/proto"
)

func init() { Register(mysqlDialer{}) }

type mysqlDialer struct{}

func (mysqlDialer) Info() DriverInfo {
	return DriverInfo{Code: "mysql", Name: "MySQL", Version: "builtin", DefaultPort: "3306"}
}

func (mysqlDialer) Connect(p proto.ConnectParams) (Session, error) {
	port := p.Port
	if port == "" {
		port = "3306"
	}
	cfg := mysql.NewConfig()
	cfg.User = p.User
	cfg.Passwd = p.Password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(p.Host, port)
	cfg.DBName = p.Database
	cfg.Timeout = 10 * time.Second
	cfg.ReadTimeout = sqlkit.Timeout
	cfg.Collation = "utf8mb4_general_ci"
	cfg.Params = map[string]string{"charset": "utf8mb4"}

	handle, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("mysql: %w", err)
	}
	// sql.Open is lazy; Ping actually establishes the connection.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		handle.Close()
		return nil, fmt.Errorf("mysql %s: %w", cfg.Addr, err)
	}

	s := &mysqlSession{db: handle}
	if v, err := s.scalar("SELECT VERSION()"); err == nil && v != nil {
		s.server = "MySQL " + *v
	}
	return s, nil
}

type mysqlSession struct {
	db     *sql.DB
	server string

	// cancel support: the statement currently running (if any) is pinned to
	// one pooled connection so KILL QUERY can target it precisely.
	mu        sync.Mutex
	activeID  int64 // CONNECTION_ID of the in-flight statement (0 = none)
	killAsked bool  // KillQuery was called for the in-flight statement

	// tx is the active manual transaction, if one has been started. While set,
	// Query/Browse/DML run inside it rather than autocommitting.
	tx *sql.Tx
}

func (s *mysqlSession) Server() string { return s.server }

// CreateDatabase creates a new MySQL/MariaDB schema. The name is backtick-quoted
// (with embedded backticks escaped) so any valid identifier is safe; charset and
// collation are restricted to identifier characters to avoid injection, and
// charset defaults to utf8mb4 when omitted.
func (s *mysqlSession) CreateDatabase(name string, opts map[string]string) error {
	q, err := buildCreateDatabaseSQL(name, opts)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(q); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

// buildCreateDatabaseSQL renders the CREATE DATABASE statement. It is a pure
// function (no I/O) so the validation and quoting can be unit-tested; the
// connection only runs the returned string.
func buildCreateDatabaseSQL(name string, opts map[string]string) (string, error) {
	if !validIdent(name) {
		return "", fmt.Errorf("invalid database name %q", name)
	}
	q := "CREATE DATABASE " + quoteIdent(name)
	charset := opts["charset"]
	if charset == "" {
		charset = "utf8mb4"
	}
	if !validIdent(charset) {
		return "", fmt.Errorf("invalid charset %q", charset)
	}
	q += " CHARACTER SET " + charset
	if collation := opts["collation"]; collation != "" {
		if !validIdent(collation) {
			return "", fmt.Errorf("invalid collation %q", collation)
		}
		q += " COLLATE " + collation
	}
	return q, nil
}

// quoteIdent backtick-quotes a SQL identifier, escaping embedded backticks.
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// validIdent reports whether s is a safe SQL identifier (letters, digits,
// underscore) — used to vet database/charset/collation names before they are
// interpolated into a CREATE DATABASE statement.
func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// scalar runs a single-value query (best effort, errors ignored by callers).
func (s *mysqlSession) scalar(q string, args ...interface{}) (*string, error) {
	var v interface{}
	if err := s.db.QueryRow(q, args...).Scan(&v); err != nil {
		return nil, err
	}
	return sqlkit.FormatValue(v), nil
}

func (s *mysqlSession) Tables() ([]proto.ObjectInfo, error) {
	rows, err := s.db.Query(`
		SELECT table_name,
		       IFNULL(table_rows, -1),
		       IF(table_type = 'VIEW', 1, 0)
		FROM information_schema.tables
		WHERE table_schema = DATABASE()
		ORDER BY table_type, table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proto.ObjectInfo
	for rows.Next() {
		var name string
		var n sql.NullInt64
		var isView bool
		if err := rows.Scan(&name, &n, &isView); err != nil {
			return nil, err
		}
		obj := proto.ObjectInfo{Name: name, Rows: n.Int64, ApproxRows: true}
		if isView {
			obj.Kind = "view"
			obj.Rows = -1
		} else {
			obj.Kind = "table"
		}
		out = append(out, obj)
	}
	return out, rows.Err()
}

func (s *mysqlSession) Columns(table string) ([]proto.ColumnInfo, error) {
	rows, err := s.db.Query(`
		SELECT column_name, data_type, is_nullable = 'YES', IFNULL(column_key, '')
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ?
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proto.ColumnInfo
	for rows.Next() {
		var c proto.ColumnInfo
		var key string
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable, &key); err != nil {
			return nil, err
		}
		if key == "PRI" {
			c.Key = "PK"
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *mysqlSession) Query(sqlText string, maxRows int) (proto.QueryResult, error) {
	// When a manual transaction is active, run inside it so the user can see
	// uncommitted changes; the transaction owns its own dedicated connection.
	s.mu.Lock()
	tx := s.tx
	s.mu.Unlock()
	if tx != nil {
		var id int64
		if err := tx.QueryRowContext(context.Background(), "SELECT CONNECTION_ID()").Scan(&id); err == nil {
			s.mu.Lock()
			s.activeID = id
			s.killAsked = false
			s.mu.Unlock()
		}
		res, err := sqlkit.Run(tx, sqlText, maxRows)
		s.mu.Lock()
		s.activeID = 0
		killed := s.killAsked
		s.mu.Unlock()
		if err != nil && killed {
			return proto.QueryResult{}, ErrQueryCancelled
		}
		return res, err
	}

	// Run the statement on a dedicated pooled connection and remember its
	// server-side thread id, so KillQuery can target it precisely without
	// disturbing other pooled connections (TablePlus process-list "Kill").
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return proto.QueryResult{}, fmt.Errorf("mysql: %w", err)
	}
	defer conn.Close()
	var id int64
	if err := conn.QueryRowContext(context.Background(), "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		id = 0
	}
	s.mu.Lock()
	s.activeID = id
	s.killAsked = false
	s.mu.Unlock()
	res, err := sqlkit.Run(conn, sqlText, maxRows)
	// The statement is no longer in flight, so no KillQuery may target it.
	// Leaving activeID set would let a later, idle KillQuery kill a stale —
	// possibly recycled — CONNECTION_ID on some other pooled connection.
	s.mu.Lock()
	s.activeID = 0
	killed := s.killAsked
	s.mu.Unlock()
	if err != nil && killed {
		// The user cancelled: surface a clean cancellation instead of the
		// driver's raw "connection was killed" noise.
		return proto.QueryResult{}, ErrQueryCancelled
	}
	return res, err
}

// KillQuery terminates the statement currently running on this session by
// issuing KILL QUERY <thread> on a separate connection.
func (s *mysqlSession) KillQuery() error {
	s.mu.Lock()
	id := s.activeID
	s.killAsked = true
	s.mu.Unlock()
	if id == 0 {
		return fmt.Errorf("mysql: no active query to kill")
	}
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("mysql kill: %w", err)
	}
	defer conn.Close()
	_, err = conn.ExecContext(context.Background(), "KILL QUERY "+strconv.FormatInt(id, 10))
	return err
}

// Browse returns the first rows of a table for the sidebar data tab.
func (s *mysqlSession) Browse(table string, limit int) (proto.QueryResult, error) {
	q := fmt.Sprintf("SELECT * FROM %s LIMIT %d", quoteMySQL(table), limit)
	s.mu.Lock()
	tx := s.tx
	s.mu.Unlock()
	if tx != nil {
		return sqlkit.Run(tx, q, limit)
	}
	return sqlkit.Run(s.db, q, limit)
}

// Metrics returns curated read-only MySQL server metrics from SHOW GLOBAL STATUS.
func (s *mysqlSession) Metrics() ([]proto.Metric, error) {
	rows, err := s.db.Query("SHOW GLOBAL STATUS WHERE Variable_name IN (" +
		"'Threads_connected','Threads_running','Uptime','Questions','Queries'," +
		"'Slow_queries','Bytes_received','Bytes_sent','Innodb_buffer_pool_read_requests'," +
		"'Innodb_buffer_pool_reads','Max_used_connections')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]string{}
	for rows.Next() {
		var name, val string
		if err := rows.Scan(&name, &val); err != nil {
			return nil, err
		}
		byName[name] = val
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	human := map[string]string{
		"Bytes_received": "Bytes received", "Bytes_sent": "Bytes sent",
		"Innodb_buffer_pool_read_requests": "InnoDB buffer reads (req)",
		"Innodb_buffer_pool_reads":         "InnoDB disk reads",
		"Max_used_connections":             "Peak connections",
	}
	label := func(k string) string {
		if h, ok := human[k]; ok {
			return h
		}
		return k
	}
	order := []string{
		"Uptime", "Threads_connected", "Threads_running", "Max_used_connections",
		"Questions", "Queries", "Slow_queries",
		"Innodb_buffer_pool_read_requests", "Innodb_buffer_pool_reads",
		"Bytes_received", "Bytes_sent",
	}
	out := make([]proto.Metric, 0, len(order))
	for _, k := range order {
		if v, ok := byName[k]; ok {
			out = append(out, proto.Metric{Label: label(k), Value: v, Group: "MySQL"})
		}
	}
	return out, nil
}

func (s *mysqlSession) Close() {
	// Roll back any transaction left open so the pooled connection goes back to
	// a clean state instead of holding a dangling transaction.
	s.mu.Lock()
	tx := s.tx
	s.tx = nil
	s.mu.Unlock()
	if tx != nil {
		_ = tx.Rollback()
	}
	s.db.Close()
}

// activeExecer returns the active manual transaction when one is open,
// otherwise the pooled handle, so row DML runs inside the transaction without
// autocommitting.
func (s *mysqlSession) activeExecer() execer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx != nil {
		return s.tx
	}
	return s.db
}

// Begin starts a manual transaction on this session.
func (s *mysqlSession) Begin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx != nil {
		return fmt.Errorf("mysql: transaction already active")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	s.tx = tx
	return nil
}

// Commit commits the active transaction, if any.
func (s *mysqlSession) Commit() error {
	s.mu.Lock()
	tx := s.tx
	s.tx = nil
	s.mu.Unlock()
	if tx == nil {
		return fmt.Errorf("mysql: no active transaction")
	}
	return tx.Commit()
}

// Rollback rolls back the active transaction, if any.
func (s *mysqlSession) Rollback() error {
	s.mu.Lock()
	tx := s.tx
	s.tx = nil
	s.mu.Unlock()
	if tx == nil {
		return fmt.Errorf("mysql: no active transaction")
	}
	return tx.Rollback()
}

// InTransaction reports whether a manual transaction is currently open.
func (s *mysqlSession) InTransaction() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tx != nil
}

// QueryStream runs a statement and emits its result in batches as rows are read
// from the cursor, so a large result is fetched incrementally with bounded
// memory. It runs inside an active manual transaction when one is open.
func (s *mysqlSession) QueryStream(sqlText string, batch int, emit func(cols []proto.ColumnInfo, rows [][]*string) (bool, error)) (proto.QueryResult, error) {
	s.mu.Lock()
	tx := s.tx
	s.mu.Unlock()
	var q sqlkit.Querier = s.db
	if tx != nil {
		q = tx
	}
	res, err := sqlkit.StreamRun(q, sqlText, batch, func(cols []proto.ColumnInfo, rows [][]*string) error {
		keep, e := emit(cols, rows)
		if e != nil {
			return e
		}
		if !keep {
			return errStreamStopped
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStreamStopped) {
		return proto.QueryResult{}, err
	}
	return proto.QueryResult{RowsAffected: res.RowsAffected, DurationMS: res.DurationMS}, nil
}

// errStreamStopped is an internal sentinel meaning the consumer asked to stop
// early (emit returned keep=false). It is not surfaced to the caller.
var errStreamStopped = errors.New("stream stopped")

// ---- row-level DML (implements db.Updater for the editable grid) ----------

func argOf(v *string) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// DefaultMarker is the sentinel *string value that tells Updater
// implementations to write the SQL keyword DEFAULT instead of a literal
// (TablePlus "Set Value → DEFAULT"). Non-MySQL updaters may treat it as text.
const DefaultMarker = "\x00DEFAULT"

// isDefault reports whether v carries the DEFAULT sentinel.
func isDefault(v *string) bool { return v != nil && *v == DefaultMarker }

// execer is the subset of *sql.DB / *sql.Tx used by the row-DML builders, so
// the same SQL is used for a single autocommit op and inside ApplyChanges'
// transaction.
type execer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func (s *mysqlSession) UpdateRow(table string, pk, changes map[string]*string) error {
	if len(changes) == 0 {
		return nil
	}
	if len(pk) == 0 {
		return fmt.Errorf("update %s: no primary key", table)
	}
	q, args := buildUpdate(table, pk, changes)
	_, err := s.activeExecer().Exec(q, args...)
	return err
}

func (s *mysqlSession) DeleteRow(table string, pk map[string]*string) error {
	if len(pk) == 0 {
		return fmt.Errorf("delete %s: no primary key", table)
	}
	q, args := buildDelete(table, pk)
	_, err := s.activeExecer().Exec(q, args...)
	return err
}

func (s *mysqlSession) InsertRow(table string, values map[string]*string) error {
	if len(values) == 0 {
		return fmt.Errorf("insert %s: no values", table)
	}
	q, args, err := buildInsert(table, values)
	if err != nil {
		return err
	}
	_, err = s.activeExecer().Exec(q, args...)
	return err
}

// ApplyChanges applies a whole commit batch inside one transaction so an error
// mid-way rolls back everything instead of leaving a partial commit. Each DML
// statement must affect exactly one row (RowsAffected == 1); anything else —
// a lost update, a row deleted underneath us, or an unexpected match — aborts
// and rolls back, mirroring the reference editor's "safe edit" guarantee. DDL
// statements are exempt from the row-count check.
func (s *mysqlSession) ApplyChanges(table string, updates []RowEdit, deletes []map[string]*string, inserts []map[string]*string, ddl []string) error {
	// If a manual transaction is active, apply inside it and leave the commit
	// to the transaction's own lifecycle (no nested BEGIN).
	s.mu.Lock()
	tx := s.tx
	s.mu.Unlock()
	if tx != nil {
		return applyWithin(tx, table, updates, deletes, inserts, ddl)
	}

	t, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer t.Rollback() // no-op once Commit succeeds
	if err := applyWithin(t, table, updates, deletes, inserts, ddl); err != nil {
		return err
	}
	return t.Commit()
}

// applyWithin runs one commit batch on an already-open transaction. Each DML
// statement must affect exactly one row (RowsAffected == 1); a mismatch aborts
// so the caller can roll back. DDL statements are exempt from the row check.
func applyWithin(x execer, table string, updates []RowEdit, deletes []map[string]*string, inserts []map[string]*string, ddl []string) error {
	for _, u := range updates {
		if len(u.Changes) == 0 {
			continue
		}
		if len(u.PK) == 0 {
			return fmt.Errorf("update %s: no primary key", table)
		}
		q, args := buildUpdate(table, u.PK, u.Changes)
		res, err := x.Exec(q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrRowMismatch
		}
	}
	for _, pk := range deletes {
		if len(pk) == 0 {
			return fmt.Errorf("delete %s: no primary key", table)
		}
		q, args := buildDelete(table, pk)
		res, err := x.Exec(q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrRowMismatch
		}
	}
	for _, vals := range inserts {
		q, args, err := buildInsert(table, vals)
		if err != nil {
			return err
		}
		res, err := x.Exec(q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrRowMismatch
		}
	}
	for _, stmt := range ddl {
		if _, err := x.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// ErrRowMismatch is returned when a DML statement did not affect exactly one
// row, indicating the target row was concurrently changed or no longer exists.
var ErrRowMismatch = errors.New("row mismatch: expected to affect exactly 1 row")

// buildUpdate renders the UPDATE statement and args shared by UpdateRow and
// ApplyChanges.
func buildUpdate(table string, pk, changes map[string]*string) (string, []interface{}) {
	var sets, where []string
	var args []interface{}
	for c, v := range changes {
		if isDefault(v) {
			sets = append(sets, quoteMySQL(c)+" = DEFAULT")
			continue
		}
		sets = append(sets, quoteMySQL(c)+" = ?")
		args = append(args, argOf(v))
	}
	for c, v := range pk {
		where = append(where, quoteMySQL(c)+" = ?")
		args = append(args, argOf(v))
	}
	q := "UPDATE " + quoteMySQL(table) + " SET " + strings.Join(sets, ", ") +
		" WHERE " + strings.Join(where, " AND ")
	return q, args
}

// buildDelete renders the DELETE statement and args shared by DeleteRow and
// ApplyChanges.
func buildDelete(table string, pk map[string]*string) (string, []interface{}) {
	var where []string
	var args []interface{}
	for c, v := range pk {
		where = append(where, quoteMySQL(c)+" = ?")
		args = append(args, argOf(v))
	}
	q := "DELETE FROM " + quoteMySQL(table) + " WHERE " + strings.Join(where, " AND ")
	return q, args
}

// buildInsert renders the INSERT statement and args shared by InsertRow and
// ApplyChanges.
func buildInsert(table string, values map[string]*string) (string, []interface{}, error) {
	var cols, ph []string
	var args []interface{}
	for c, v := range values {
		if isDefault(v) {
			continue // omit the column so the server applies its default
		}
		cols = append(cols, quoteMySQL(c))
		ph = append(ph, "?")
		args = append(args, argOf(v))
	}
	if len(cols) == 0 {
		return "", nil, fmt.Errorf("insert %s: no values", table)
	}
	q := "INSERT INTO " + quoteMySQL(table) + " (" + strings.Join(cols, ", ") +
		") VALUES (" + strings.Join(ph, ", ") + ")"
	return q, args, nil
}

func quoteMySQL(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
