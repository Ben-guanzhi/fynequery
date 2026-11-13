package db

// Built-in MySQL driver (default database type, compiled into the app).
// MariaDB servers speak the same protocol and work through this driver too.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dbcore/dbcore"
	dbcoremysql "github.com/dbcore/dbcore/mysql"
	"github.com/dbcore/dbcore/sqltext"

	"fynequery/internal/sqlkit"
	"fynequery/proto"
)

func init() { Register(mysqlDialer{}) }

const (
	// mysqlConnMaxLifetime 是池中单个物理连接的最长存活时间；到期后即使仍被
	// 引用也会被关闭并重建，避免服务端 socket / 会话状态无限期驻留。
	mysqlConnMaxLifetime = 30 * time.Minute
	// mysqlConnMaxIdleTime 是空闲连接的最长保留时间；超过后被池关闭，服务端
	// 侧的空闲连接随之释放。
	mysqlConnMaxIdleTime = 5 * time.Minute
)

type mysqlDialer struct{}

func (mysqlDialer) Info() DriverInfo {
	return DriverInfo{Code: "mysql", Name: "MySQL", Version: "builtin", DefaultPort: "3306"}
}

func (mysqlDialer) Connect(p proto.ConnectParams) (Session, error) {
	port := p.Port
	if port == "" {
		port = "3306"
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("mysql: bad port %q: %w", port, err)
	}
	// The DSN comes from the shared kernel so this client and TableProGo cannot
	// drift on quoting, TLS mapping or timeout parameters. The values below are
	// fynequery's own session policy (statement timeout, utf8mb4, no cert
	// verification for "required" TLS), passed as a ConnSpec.
	spec := dbcore.ConnSpec{
		Kind:           dbcore.KindMySQL,
		Host:           p.Host,
		Port:           portNum,
		User:           p.User,
		Password:       p.Password,
		Database:       p.Database,
		SSLMode:        p.Options["sslmode"],
		ConnectTimeout: 10 * time.Second,
		Params: map[string]string{
			"charset":     "utf8mb4",
			"collation":   "utf8mb4_general_ci",
			"readTimeout": sqlkit.Timeout.String(),
		},
	}

	handle, err := sql.Open(dbcoremysql.DriverName, dbcoremysql.DSN(spec))
	if err != nil {
		return nil, fmt.Errorf("mysql: %w", err)
	}
	// Admission control: bound the pool explicitly instead of letting however
	// many concurrent tabs/queries are open hit the server until its
	// max_connections rejects them. The budget leaves room for the pattern
	// Query uses — one pinned connection for the running statement plus a
	// second independent connection for KILL QUERY — while catalog reads and
	// keepalive pings run alongside.
	handle.SetMaxOpenConns(8)
	handle.SetMaxIdleConns(4)
	// 连接级 TTL：空闲服务端 socket 不能比 session 活得更久。没有这两个设置时，
	// 被池保留的旧连接会一直占用服务端连接，直到进程退出。
	handle.SetConnMaxLifetime(mysqlConnMaxLifetime)
	handle.SetConnMaxIdleTime(mysqlConnMaxIdleTime)
	// sql.Open is lazy; Ping actually establishes the connection.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		handle.Close()
		return nil, fmt.Errorf("mysql %s: %w", spec.Addr(), err)
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
	// Query/Browse/DML run inside it rather than autocommitting. txConn is the
	// connection that tx was opened on (pinned out of the pool, see Begin) and
	// must go back to the pool when the tx settles.
	tx     *sql.Tx
	txConn *sql.Conn
	// batchTx marks a commit batch that owns the session's write path while it
	// runs its own transaction, so a concurrent Begin refuses instead of
	// splitting one commit across two transactions that cannot see each other
	// (N-03).
	batchTx bool
	// txTimer is the hard ceiling on one manual transaction. A forgotten BEGIN
	// pins a connection and its pool admission slot until the session closes,
	// so enough abandoned BEGINs exhaust the pool (N-04).
	txTimer *time.Timer
	// txExpired records that the ceiling rolled the transaction back, so the
	// next Commit/Rollback can say why there is nothing left to settle.
	txExpired bool
}

// txMaxLifetime bounds one manual transaction (N-04).
const txMaxLifetime = 10 * time.Minute

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
	if q, err := sqltext.QuoteIdent("mysql", name); err == nil {
		return q
	}
	// The name was refused by the guard (empty / overlong / control-carrying);
	// still emit a native identifier rather than an ANSI one.
	return sqltext.QuoteIdentLoose("mysql", name)
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

// catalogCtx bounds metadata round-trips (Tables/Columns/KILL/Metrics): they
// serve UI refreshes from background goroutines, and an unbounded call against
// a slow server parked the worker (and whatever refresh it was doing) forever.
func catalogCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// acquireConn takes a pooled connection with a bounded WAIT: the per-session
// pool is capped (mysqlMaxOpenConns), so saturation is possible and an
// unbounded wait parked the caller — often a UI worker — forever. Only the
// wait for the connection is bounded; work done ON the connection keeps
// whatever deadline its caller chose. The caller must conn.Close().
func (s *mysqlSession) acquireConn() (*sql.Conn, error) {
	ctx, cancel := catalogCtx()
	defer cancel()
	return s.db.Conn(ctx)
}

func (s *mysqlSession) Tables() ([]proto.ObjectInfo, error) {
	// An empty schema resolves to DATABASE(), which is what the session is bound to.
	ctx, cancel := catalogCtx()
	defer cancel()
	found, err := dbcoremysql.NewFromDB(s.db).ListTables(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]proto.ObjectInfo, 0, len(found))
	for _, t := range found {
		out = append(out, proto.ObjectInfo{
			Name: t.Name, Kind: t.Type, Rows: t.Rows, ApproxRows: t.ApproxRows,
		})
	}
	return out, nil
}

func (s *mysqlSession) Columns(table string) ([]proto.ColumnInfo, error) {
	// DATABASE() is the schema, so an empty schema resolves to the connected DB.
	ctx, cancel := catalogCtx()
	defer cancel()
	found, err := dbcoremysql.NewFromDB(s.db).ListColumns(ctx, "", table)
	if err != nil {
		return nil, err
	}
	out := make([]proto.ColumnInfo, 0, len(found))
	for _, c := range found {
		col := proto.ColumnInfo{Name: c.Name, Type: c.Type, Nullable: c.Nullable}
		if c.IsPrimaryKey {
			col.Key = "PK"
		}
		out = append(out, col)
	}
	return out, nil
}

// Query 包一层可用性记录（S-01）：成功/取消/失败三态与耗时都记在驱动层，
// 于是所有调用方（编辑器、DDL、备份、ERD…）自动被覆盖。
func (s *mysqlSession) Query(sqlText string, maxRows int) (proto.QueryResult, error) {
	started := time.Now()
	res, err := s.queryOnce(sqlText, maxRows)
	outcome := "ok"
	switch {
	case errors.Is(err, ErrQueryCancelled):
		outcome = "cancelled"
	case err != nil:
		outcome = "error"
	}
	record("query", outcome, started, map[string]string{
		"statement_type": stmtKeyword(sqlText),
		"rows":           strconv.Itoa(len(res.Rows)),
		"truncated":      "false",
	})
	return res, err
}

func (s *mysqlSession) queryOnce(sqlText string, maxRows int) (proto.QueryResult, error) {
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
	// Only the WAIT for the connection is bounded (see acquireConn); the
	// statement itself runs without a deadline — a long query is legitimate.
	conn, err := s.acquireConn()
	if err != nil {
		return proto.QueryResult{}, fmt.Errorf("mysql: %w", err)
	}
	defer conn.Close()
	var id int64
	idCtx, idCancel := catalogCtx()
	if err := conn.QueryRowContext(idCtx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		id = 0
	}
	idCancel()
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
	ctx, cancel := catalogCtx()
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("mysql kill: %w", err)
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "KILL QUERY "+strconv.FormatInt(id, 10))
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
	ctx, cancel := catalogCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "SHOW GLOBAL STATUS WHERE Variable_name IN ("+
		"'Threads_connected','Threads_running','Uptime','Questions','Queries',"+
		"'Slow_queries','Bytes_received','Bytes_sent','Innodb_buffer_pool_read_requests',"+
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
	conn := s.txConn
	timer := s.txTimer
	s.tx, s.txConn, s.txTimer = nil, nil, nil
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if tx != nil {
		_ = tx.Rollback()
	}
	if conn != nil {
		conn.Close()
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
	// Bound the WAIT for a connection, not the transaction: a BeginTx context
	// governs the transaction for its whole life and would roll a legitimate
	// long manual transaction back at the deadline. Pin the connection with a
	// bounded wait, open the transaction on it without one, and keep the
	// pinned connection until the tx settles (Commit/Rollback/Close).
	conn, err := s.acquireConn()
	if err != nil {
		return fmt.Errorf("mysql: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx != nil {
		conn.Close()
		return fmt.Errorf("mysql: transaction already active")
	}
	// A commit batch runs its own transaction on the session's write path; a
	// Begin racing it would put one write inside the manual transaction and the
	// other on a pool connection of its own (N-03).
	if s.batchTx {
		conn.Close()
		return fmt.Errorf("mysql: a commit batch is in flight; retry when it finishes")
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mysql: %w", err)
	}
	s.tx = tx
	s.txConn = conn
	s.txExpired = false
	s.txTimer = time.AfterFunc(txMaxLifetime, func() { s.expireTx(tx) })
	return nil
}

// expireTx rolls back a manual transaction that outlived txMaxLifetime. The
// identity check keeps a late fire from touching a transaction that was
// committed, rolled back or replaced in the meantime.
func (s *mysqlSession) expireTx(tx *sql.Tx) {
	s.mu.Lock()
	if s.tx != tx {
		s.mu.Unlock()
		return
	}
	conn := s.txConn
	s.tx, s.txConn, s.txTimer = nil, nil, nil
	s.txExpired = true
	s.mu.Unlock()
	_ = tx.Rollback()
	if conn != nil {
		conn.Close()
	}
}

// Commit commits the active transaction, if any.
func (s *mysqlSession) Commit() error {
	started := time.Now()
	err := s.commitOnce()
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	record("commit", outcome, started, nil)
	return err
}

func (s *mysqlSession) commitOnce() error {
	s.mu.Lock()
	tx := s.tx
	conn := s.txConn
	timer := s.txTimer
	expired := s.txExpired
	s.tx = nil
	s.txConn = nil
	s.txTimer = nil
	s.txExpired = false
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if tx == nil {
		if expired {
			return fmt.Errorf("mysql: transaction expired and was rolled back")
		}
		return fmt.Errorf("mysql: no active transaction")
	}
	err := tx.Commit()
	if conn != nil {
		conn.Close() // the pinned connection goes back to the pool
	}
	return err
}

// Rollback rolls back the active transaction, if any.
func (s *mysqlSession) Rollback() error {
	started := time.Now()
	err := s.rollbackOnce()
	outcome := "ok"
	if err != nil {
		outcome = "error"
	} else {
		// 成功的回滚是任务级回退：契约要求 outcome=reverted。
		outcome = "reverted"
	}
	record("rollback", outcome, started, nil)
	return err
}

func (s *mysqlSession) rollbackOnce() error {
	s.mu.Lock()
	tx := s.tx
	conn := s.txConn
	timer := s.txTimer
	expired := s.txExpired
	s.tx = nil
	s.txConn = nil
	s.txTimer = nil
	s.txExpired = false
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if tx == nil {
		if expired {
			return fmt.Errorf("mysql: transaction expired and was rolled back")
		}
		return fmt.Errorf("mysql: no active transaction")
	}
	err := tx.Rollback()
	if conn != nil {
		conn.Close() // the pinned connection goes back to the pool
	}
	return err
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
	res, err := sqlkit.StreamRun(q, sqlText, batch, func(cols []proto.ColumnInfo, rows [][]proto.CellPayload) error {
		keep, e := emit(cols, taggedRowsToStrings(rows))
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
//
// 0 rows from a keyed UPDATE is always a conflict here, never a same-value
// no-op: this client's DSN sets ClientFoundRows (dbcore/mysql/spec.go), so
// RowsAffected counts MATCHED rows — a matched-but-identical UPDATE reports
// 1. A zero-row probe on top of that would be dead code at best and, in the
// delete/reinsert race window, would silently skip a user edit instead of
// rolling back (R2-02 的教训要按本驱动的 matched 语义记,不按 changed 语义).
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
	// to the transaction's own lifecycle (no nested BEGIN). Otherwise claim the
	// session's write path for the batch's own transaction, so a concurrent
	// Begin refuses instead of racing it (N-03).
	s.mu.Lock()
	tx := s.tx
	if tx != nil {
		s.mu.Unlock()
		return applyWithin(tx, table, updates, deletes, inserts, ddl)
	}
	if s.batchTx {
		s.mu.Unlock()
		return fmt.Errorf("mysql: a commit batch is already in flight")
	}
	s.batchTx = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.batchTx = false
		s.mu.Unlock()
	}()

	// Bound the wait for a connection, not the transaction (see Begin): the
	// commit batch lives on a pinned connection opened without a deadline,
	// and the pin is local to this call.
	conn, err := s.acquireConn()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer conn.Close()
	t, err := conn.BeginTx(context.Background(), nil)
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
//
// With ClientFoundRows on (see execer), a keyed UPDATE reporting 0 rows means
// the row was not there — there is no legitimate same-value no-op to probe
// for, so 0 is always ErrRowMismatch.
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
		// A NULL key value addresses the row with IS NULL: "col = NULL" is
		// UNKNOWN in SQL and matches nothing, so the edit would report a
		// concurrent change (ErrRowMismatch) for a row that is right there.
		// SQLite's TEXT PRIMARY KEY admits NULL, and a synthetic snapshot
		// column can produce one.
		if v == nil {
			where = append(where, quoteMySQL(c)+" IS NULL")
			continue
		}
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
		if v == nil {
			where = append(where, quoteMySQL(c)+" IS NULL")
			continue
		}
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
	return quoteIdent(name)
}

// ListIndexes implements IndexLister through the shared kernel driver, so the
// Indexes pane never has to know information_schema.STATISTICS' layout (nor its
// inline-literal quoting rules).
func (s *mysqlSession) ListIndexes(schema, table string) ([]dbcore.IndexInfo, error) {
	drv, ok := dbcoremysql.NewFromDB(s.db).AsSQL()
	if !ok {
		return nil, dbcore.ErrNotSupported
	}
	// catalogCtx, not context.Background(): ListIndexes serves the Indexes pane
	// from a background refresh, and an unbounded information_schema query
	// against a slow server parked the worker forever (every other metadata
	// path in this file already bounds itself).
	ctx, cancel := catalogCtx()
	defer cancel()
	return drv.ListIndexes(ctx, schema, table)
}
