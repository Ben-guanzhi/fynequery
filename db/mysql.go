package db

// Built-in MySQL driver (default database type, compiled into the app).
// MariaDB servers speak the same protocol and work through this driver too.

import (
	"context"
	"database/sql"
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
}

func (s *mysqlSession) Server() string { return s.server }

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
	return sqlkit.Run(s.db, fmt.Sprintf("SELECT * FROM %s LIMIT %d", quoteMySQL(table), limit), limit)
}

func (s *mysqlSession) Close() { s.db.Close() }

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
	_, err := s.db.Exec(q, args...)
	return err
}

func (s *mysqlSession) DeleteRow(table string, pk map[string]*string) error {
	if len(pk) == 0 {
		return fmt.Errorf("delete %s: no primary key", table)
	}
	q, args := buildDelete(table, pk)
	_, err := s.db.Exec(q, args...)
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
	_, err = s.db.Exec(q, args...)
	return err
}

// ApplyChanges applies a whole commit batch inside one transaction so an error
// mid-way rolls back everything instead of leaving a partial commit.
func (s *mysqlSession) ApplyChanges(table string, updates []RowEdit, deletes []map[string]*string, inserts []map[string]*string, ddl []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() // no-op once Commit succeeds

	for _, u := range updates {
		if len(u.Changes) == 0 {
			continue
		}
		if len(u.PK) == 0 {
			return fmt.Errorf("update %s: no primary key", table)
		}
		q, args := buildUpdate(table, u.PK, u.Changes)
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	for _, pk := range deletes {
		if len(pk) == 0 {
			return fmt.Errorf("delete %s: no primary key", table)
		}
		q, args := buildDelete(table, pk)
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	for _, vals := range inserts {
		q, args, err := buildInsert(table, vals)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	for _, stmt := range ddl {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

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
