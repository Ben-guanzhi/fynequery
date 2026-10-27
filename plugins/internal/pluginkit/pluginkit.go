// Package pluginkit is the shared skeleton for fynequery plugin
// executables: connection bookkeeping and the protocol serve loop. A plugin
// main supplies a Dialect (driver-specific SQL) and is otherwise one call.
package pluginkit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"fynequery/internal/sqlkit"
	"fynequery/proto"
)

// Dialect supplies the driver-specific behavior of a plugin.
type Dialect interface {
	Code() string        // "postgres", "mssql", "oracle"...
	Name() string        // "PostgreSQL", "SQL Server"...
	Version() string     // plugin version string
	DefaultPort() string // "5432"...
	// Open dials the server, verifies the connection (Ping) and reports a
	// server identification string.
	Open(p proto.ConnectParams) (*sql.DB, string, error)
	// ListTables returns tables and views of the connected database.
	ListTables(db *sql.DB) ([]proto.ObjectInfo, error)
	// ListColumns returns column metadata; Key="PK" marks primary keys.
	ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error)
	// BrowseSQL builds a "first N rows of table" statement in the dialect.
	BrowseSQL(table string, limit int) string
	// CreateDatabase creates a new database/schema on the server using the
	// already-opened connection handle. Returns an error for engines that
	// cannot create databases from an ordinary session (e.g. Oracle) so the UI
	// can surface a clear message.
	CreateDatabase(db *sql.DB, name, charset, collation string) error
	// QuoteIdent quotes a single SQL identifier (table or column name) for the
	// dialect, escaping any embedded quote characters. Used by the shared DML
	// builders so UPDATE/INSERT/DELETE are generated in one place.
	QuoteIdent(name string) string
	// Placeholder renders the i-th positional parameter (1-based) for the
	// dialect's driver (?, $1, @p1, :1, ...).
	Placeholder(i int) string
	// Metrics returns a curated set of read-only server metrics for the
	// connected database (connections, uptime, cache hit ratio, ...). Engines
	// that expose little return a short list; the host treats an empty list as
	// "no metrics available".
	Metrics(db *sql.DB) ([]proto.Metric, error)
}

// Main runs the plugin until the host closes stdin. Never returns.
func Main(d Dialect) {
	h := newHandler(d)
	// The banner names the engine in both vocabularies: "code" for the fynequery
	// host, "kind" for a kernel host. One plugin, either client.
	banner := proto.Banner{
		Proto:   proto.ProtocolName,
		Version: proto.ProtocolVersion,
		Code:    d.Code(),
		Name:    d.Name(),
		Kernel:  d.Code(),
	}
	if err := proto.Serve(h, banner); err != nil {
		log.Fatalf("serve: %v", err)
	}
	// stdin closed: host is gone, exit quietly.
	os.Exit(0)
}

// OpenVerify opens a database/sql connection, pings it with a timeout and,
// when versionQuery is not empty, reads one scalar for the server banner.
// It replaces the Open boilerplate shared by every dialect.
func OpenVerify(driverName, dsn, versionQuery string) (*sql.DB, string, error) {
	handle, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		handle.Close()
		return nil, "", err
	}
	if versionQuery == "" {
		return handle, "", nil
	}
	var v interface{}
	if err := handle.QueryRowContext(ctx, versionQuery).Scan(&v); err != nil {
		return handle, "", nil // server string is best effort
	}
	if s := sqlkit.FormatValue(v); s != nil {
		return handle, *s, nil
	}
	return handle, "", nil
}

type handler struct {
	dialect Dialect
	mu      sync.Mutex
	conns   map[string]*sql.DB
	txs     map[string]*sql.Tx // active manual transactions, keyed by ConnID
	server  map[string]string
	next    int

	// runMu guards inflight: the statements currently running per connection,
	// so a Cancel request can cancel them while their own request is still
	// being served (serve runs each request in its own goroutine).
	runMu    sync.Mutex
	inflight map[string]map[int]context.CancelFunc
	runSeq   int
}

// errQueryCancelled is what a cancelled statement reports, matching the host's
// db.ErrQueryCancelled vocabulary so the UI can show "cancelled" instead of a
// driver error.
var errQueryCancelled = errors.New("query cancelled")

// newHandler builds a handler with every map initialized; Open writes to both
// conns and server, so a missing map would panic on the first success.
func newHandler(d Dialect) *handler {
	return &handler{
		dialect:  d,
		conns:    map[string]*sql.DB{},
		txs:      map[string]*sql.Tx{},
		server:   map[string]string{},
		inflight: map[string]map[int]context.CancelFunc{},
	}
}

// trackRun registers a cancellable context for one statement on connID. The
// returned done func must be deferred: it deregisters the run so a later
// Cancel cannot cancel a statement that has already finished (and so Cancel
// with nothing in flight can report that honestly).
func (h *handler) trackRun(connID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	h.runMu.Lock()
	runs := h.inflight[connID]
	if runs == nil {
		runs = map[int]context.CancelFunc{}
		h.inflight[connID] = runs
	}
	h.runSeq++
	seq := h.runSeq
	runs[seq] = cancel
	h.runMu.Unlock()
	return ctx, func() {
		cancel()
		h.runMu.Lock()
		delete(runs, seq)
		h.runMu.Unlock()
	}
}

// Cancel cancels every statement running on the connection the params name.
func (h *handler) Cancel(p proto.CancelParams) error {
	h.runMu.Lock()
	runs := h.inflight[p.ConnID]
	cancels := make([]context.CancelFunc, 0, len(runs))
	for _, cancel := range runs {
		cancels = append(cancels, cancel)
	}
	h.runMu.Unlock()
	if len(cancels) == 0 {
		return fmt.Errorf("no active query on connection %q", p.ConnID)
	}
	for _, cancel := range cancels {
		cancel()
	}
	return nil
}

// cancelled reports whether err is the statement's death by Cancel rather than
// a server error: the context the statement ran under is the authority (the
// driver's own error text varies by engine).
func cancelled(ctx context.Context, err error) bool {
	return err != nil && ctx.Err() != nil
}

func (h *handler) Meta() proto.MetaResult {
	return proto.MetaResult{
		Code:        h.dialect.Code(),
		Name:        h.dialect.Name(),
		Version:     h.dialect.Version(),
		DefaultPort: h.dialect.DefaultPort(),
	}
}

func (h *handler) Open(p proto.ConnectParams) (proto.OpenResult, error) {
	handle, server, err := h.dialect.Open(p)
	if err != nil {
		return proto.OpenResult{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := fmt.Sprintf("c%d", h.next)
	h.conns[id] = handle
	h.server[id] = server
	return proto.OpenResult{ConnID: id, Server: server}, nil
}

func (h *handler) Close(p proto.CloseParams) error {
	h.mu.Lock()
	handle, ok := h.conns[p.ConnID]
	delete(h.conns, p.ConnID)
	delete(h.server, p.ConnID)
	if tx, ok := h.txs[p.ConnID]; ok {
		delete(h.txs, p.ConnID)
		// Roll back any transaction left open so the server connection does
		// not leak a dangling transaction after the host goes away.
		h.mu.Unlock()
		_ = tx.Rollback()
		if handle != nil {
			return handle.Close()
		}
		return nil
	}
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown connection %q", p.ConnID)
	}
	return handle.Close()
}

func (h *handler) conn(id string) (*sql.DB, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	handle, ok := h.conns[id]
	if !ok {
		return nil, fmt.Errorf("unknown connection %q (closed?)", id)
	}
	return handle, nil
}

// querier returns the active transaction for a connection when one is open,
// otherwise its *sql.DB. Both satisfy sqlkit.Querier so callers can run
// statements without caring which is in effect.
func (h *handler) querier(id string) (sqlkit.Querier, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if tx, ok := h.txs[id]; ok {
		return tx, nil
	}
	handle, ok := h.conns[id]
	if !ok {
		return nil, fmt.Errorf("unknown connection %q (closed?)", id)
	}
	return handle, nil
}

func (h *handler) Tables(p proto.TablesParams) (proto.TablesResult, error) {
	handle, err := h.conn(p.ConnID)
	if err != nil {
		return proto.TablesResult{}, err
	}
	objs, err := h.dialect.ListTables(handle)
	return proto.TablesResult{Objects: objs}, err
}

func (h *handler) Columns(p proto.ColumnsParams) (proto.ColumnsResult, error) {
	handle, err := h.conn(p.ConnID)
	if err != nil {
		return proto.ColumnsResult{}, err
	}
	cols, err := h.dialect.ListColumns(handle, p.Table)
	return proto.ColumnsResult{Columns: cols}, err
}

func (h *handler) Query(p proto.QueryParams) (proto.QueryResult, error) {
	q, err := h.querier(p.ConnID)
	if err != nil {
		return proto.QueryResult{}, err
	}
	ctx, done := h.trackRun(p.ConnID)
	defer done()
	res, err := sqlkit.RunCtx(q, ctx, p.SQL, p.MaxRows)
	if cancelled(ctx, err) {
		return proto.QueryResult{}, errQueryCancelled
	}
	return res, err
}

func (h *handler) Browse(p proto.BrowseParams) (proto.QueryResult, error) {
	q, err := h.querier(p.ConnID)
	if err != nil {
		return proto.QueryResult{}, err
	}
	if p.Limit <= 0 {
		p.Limit = 1000
	}
	ctx, done := h.trackRun(p.ConnID)
	defer done()
	res, err := sqlkit.RunCtx(q, ctx, h.dialect.BrowseSQL(p.Table, p.Limit), p.Limit)
	if cancelled(ctx, err) {
		return proto.QueryResult{}, errQueryCancelled
	}
	return res, err
}

// CreateDatabase looks up the opened connection and delegates to the dialect.
func (h *handler) CreateDatabase(p proto.CreateDBParams) error {
	handle, err := h.conn(p.ConnID)
	if err != nil {
		return err
	}
	return h.dialect.CreateDatabase(handle, p.Name, p.Charset, p.Collation)
}

// ---- row-level DML (implements proto.Handler for the editable grid) --------
//
// The SQL for UPDATE/INSERT/DELETE is generated centrally here using the
// dialect's QuoteIdent/Placeholder, so every engine shares one correct
// implementation instead of each plugin hand-rolling DML. This is the same
// "single source of truth" idea the reference design uses for its built-in
// drivers.

// Update applies one UPDATE and reports how many rows it touched.
func (h *handler) Update(p proto.DMLSingleParams) (proto.DMLResult, error) {
	q, err := h.querier(p.ConnID)
	if err != nil {
		return proto.DMLResult{}, err
	}
	stmt, args := buildUpdate(h.dialect, p.Table, cellStrings(p.PK), cellStrings(p.Changes))
	return runDML(q, stmt, args)
}

// Insert applies one INSERT and reports how many rows it touched.
func (h *handler) Insert(p proto.DMLSingleParams) (proto.DMLResult, error) {
	q, err := h.querier(p.ConnID)
	if err != nil {
		return proto.DMLResult{}, err
	}
	stmt, args, err := buildInsert(h.dialect, p.Table, cellStrings(p.Values))
	if err != nil {
		return proto.DMLResult{}, err
	}
	return runDML(q, stmt, args)
}

// Delete applies one DELETE and reports how many rows it touched.
func (h *handler) Delete(p proto.DMLSingleParams) (proto.DMLResult, error) {
	q, err := h.querier(p.ConnID)
	if err != nil {
		return proto.DMLResult{}, err
	}
	stmt, args := buildDelete(h.dialect, p.Table, cellStrings(p.PK))
	return runDML(q, stmt, args)
}

// ApplyBatch applies a whole commit. When a manual transaction is active on the
// connection the statements run inside it (no commit/rollback here); otherwise
// they run in a freshly opened, atomically committed transaction. Each DML
// statement must affect exactly one row (RowsAffected == 1); anything else is
// treated as a lost-update / collision and the whole batch rolls back,
// mirroring the reference editor's "safe edit" guarantee. A transient
// "database is locked"/"busy" condition (notably SQLite under concurrent
// writers) is retried a few times before giving up.
func (h *handler) ApplyBatch(p proto.DMLBatchParams) (proto.DMLResult, error) {
	var stmts []batchStmt
	for _, u := range p.Updates {
		q, a := buildUpdate(h.dialect, p.Table, cellStrings(u.PK), cellStrings(u.Changes))
		stmts = append(stmts, batchStmt{q, a, true})
	}
	for _, pk := range p.Deletes {
		q, a := buildDelete(h.dialect, p.Table, cellStrings(pk))
		stmts = append(stmts, batchStmt{q, a, true})
	}
	for _, vals := range p.Inserts {
		q, a, err := buildInsert(h.dialect, p.Table, cellStrings(vals))
		if err != nil {
			return proto.DMLResult{}, err
		}
		stmts = append(stmts, batchStmt{q, a, true})
	}
	for _, ddl := range p.DDL {
		stmts = append(stmts, batchStmt{ddl, nil, false})
	}

	// If a manual transaction is active, run inside it and let the host's
	// Commit/Rollback own the lifecycle.
	h.mu.Lock()
	if tx, ok := h.txs[p.ConnID]; ok {
		h.mu.Unlock()
		total, err := runBatchTx(tx, stmts)
		return proto.DMLResult{RowsAffected: total}, err
	}
	h.mu.Unlock()

	handle, err := h.conn(p.ConnID)
	if err != nil {
		return proto.DMLResult{}, err
	}

	var total int64
	const maxBusyRetries = 5
	for attempt := 0; ; attempt++ {
		total, err = runBatchOpen(handle, stmts)
		if err == nil {
			break
		}
		if errors.Is(err, errBusy) && attempt < maxBusyRetries {
			time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
			continue
		}
		break
	}
	return proto.DMLResult{RowsAffected: total}, err
}

// ---- manual transactions (TablePlus "Begin Transaction" mode) --------------

// Begin starts a transaction on the connection. A second Begin without an
// intervening Commit/Rollback fails.
func (h *handler) Begin(p proto.TxParams) error {
	h.mu.Lock()
	if _, ok := h.txs[p.ConnID]; ok {
		h.mu.Unlock()
		return fmt.Errorf("transaction already active")
	}
	handle, ok := h.conns[p.ConnID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("unknown connection %q (closed?)", p.ConnID)
	}
	h.mu.Unlock()
	tx, err := handle.Begin()
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.txs[p.ConnID] = tx
	h.mu.Unlock()
	return nil
}

// Commit commits the active transaction, if any.
func (h *handler) Commit(p proto.TxParams) error {
	h.mu.Lock()
	tx, ok := h.txs[p.ConnID]
	delete(h.txs, p.ConnID)
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("no active transaction")
	}
	return tx.Commit()
}

// Rollback rolls back the active transaction, if any.
func (h *handler) Rollback(p proto.TxParams) error {
	h.mu.Lock()
	tx, ok := h.txs[p.ConnID]
	delete(h.txs, p.ConnID)
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("no active transaction")
	}
	return tx.Rollback()
}

// ---- streaming results -----------------------------------------------------

// QueryStream runs a statement and emits its result in batches as rows are read
// from the cursor, so a large result is fetched incrementally with bounded
// memory. It runs inside an active transaction when one is open, and is
// cancellable like Query (the tracked context aborts both the query and the
// cursor walk).
func (h *handler) QueryStream(p proto.QueryStreamParams, emit func(proto.StreamFrame) error) (proto.QueryResult, error) {
	q, err := h.querier(p.ConnID)
	if err != nil {
		return proto.QueryResult{}, err
	}
	ctx, done := h.trackRun(p.ConnID)
	defer done()
	res, err := sqlkit.StreamRunCtx(q, ctx, p.SQL, p.Batch, func(cols []proto.ColumnInfo, rows [][]proto.CellPayload) error {
		return emit(proto.StreamFrame{Columns: cols, Rows: rows})
	})
	if cancelled(ctx, err) {
		return proto.QueryResult{}, errQueryCancelled
	}
	if err != nil {
		return proto.QueryResult{}, err
	}
	return proto.QueryResult{RowsAffected: res.RowsAffected, DurationMS: res.DurationMS}, nil
}

// ---- server metrics --------------------------------------------------------

// Metrics returns the dialect's curated server metrics for a connection.
func (h *handler) Metrics(p proto.MetricsParams) (proto.MetricsResult, error) {
	handle, err := h.conn(p.ConnID)
	if err != nil {
		return proto.MetricsResult{}, err
	}
	ms, err := h.dialect.Metrics(handle)
	if err != nil {
		return proto.MetricsResult{}, err
	}
	return proto.MetricsResult{Metrics: ms}, nil
}

// KVMetrics runs a two-column (label, value) query and formats the rows as
// metrics under group. Engines format the value in SQL (so this stays generic);
// an empty result yields no metrics.
func KVMetrics(db *sql.DB, group, query string) ([]proto.Metric, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []proto.Metric
	for rows.Next() {
		var label, value sql.NullString
		if err := rows.Scan(&label, &value); err != nil {
			return nil, err
		}
		out = append(out, proto.Metric{Label: label.String, Value: value.String, Group: group})
	}
	return out, rows.Err()
}
