package pluginkit

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for the handler test

	"fynequery/proto"
)

type nilMapDialect struct{}

func (nilMapDialect) Code() string        { return "test" }
func (nilMapDialect) Name() string        { return "Test" }
func (nilMapDialect) Version() string     { return "0" }
func (nilMapDialect) DefaultPort() string { return "0" }

func (nilMapDialect) Open(proto.ConnectParams) (*sql.DB, string, error)       { return nil, "", nil }
func (nilMapDialect) ListTables(*sql.DB) ([]proto.ObjectInfo, error)          { return nil, nil }
func (nilMapDialect) ListColumns(*sql.DB, string) ([]proto.ColumnInfo, error) { return nil, nil }
func (nilMapDialect) BrowseSQL(string, int) string                            { return "" }
func (nilMapDialect) CreateDatabase(*sql.DB, string, string, string) error    { return nil }
func (nilMapDialect) QuoteIdent(name string) string                           { return name }
func (nilMapDialect) Placeholder(int) string                                  { return "?" }
func (nilMapDialect) Metrics(*sql.DB) ([]proto.Metric, error)                 { return nil, nil }

func sp(s string) *string { return &s }

// TestDMLBuilders exercises the centralized UPDATE/INSERT/DELETE generation so
// a regression in quoting or placeholder numbering is caught without a live
// database. The dialect here is trivial (identity quoting, "?"), which keeps
// the assertions about structure rather than engine specifics.
func TestDMLBuilders(t *testing.T) {
	d := nilMapDialect{}

	// UPDATE with a changed column plus a two-column primary key. Map
	// iteration order is randomised, so the WHERE predicates (and their args)
	// may appear in either order — assert the set, not a fixed string.
	q, args := buildUpdate(d, "t", map[string]*string{"id": sp("1"), "k": sp("2")},
		map[string]*string{"name": sp("x")})
	if !strings.HasPrefix(q, "UPDATE t SET name = ? WHERE ") ||
		!strings.Contains(q, "id = ?") || !strings.Contains(q, "k = ?") ||
		!strings.Contains(q, " AND ") {
		t.Fatalf("update sql = %q", q)
	}
	if len(args) != 3 || args[0].(string) != "x" {
		t.Fatalf("update args = %v", args)
	}
	pkVals := map[string]bool{args[1].(string): true, args[2].(string): true}
	if !pkVals["1"] || !pkVals["2"] {
		t.Fatalf("update pk args = %v", args)
	}

	// A DEFAULT sentinel in changes emits the keyword, not a placeholder.
	q, args = buildUpdate(d, "t", map[string]*string{"id": sp("1")},
		map[string]*string{"v": sp(proto.DefaultMarker)})
	if q != "UPDATE t SET v = DEFAULT WHERE id = ?" {
		t.Fatalf("update default sql = %q", q)
	}
	if len(args) != 1 {
		t.Fatalf("update default args = %v", args)
	}

	// DELETE.
	q, args = buildDelete(d, "t", map[string]*string{"id": sp("9")})
	if q != "DELETE FROM t WHERE id = ?" || len(args) != 1 {
		t.Fatalf("delete = %q %v", q, args)
	}

	// INSERT omits DEFAULT columns so the server applies its own default.
	q, args, err := buildInsert(d, "t", map[string]*string{"a": sp("1"), "b": sp(proto.DefaultMarker)})
	if err != nil {
		t.Fatalf("insert err = %v", err)
	}
	if q != "INSERT INTO t (a) VALUES (?)" || len(args) != 1 {
		t.Fatalf("insert = %q %v", q, args)
	}

	// INSERT with nothing to insert is an error.
	if _, _, err := buildInsert(d, "t", map[string]*string{"b": sp(proto.DefaultMarker)}); err == nil {
		t.Fatal("expected insert error when all values are DEFAULT")
	}
}

// TestNewHandlerInitializesMaps guards against the crash where Open wrote to
// handler.server/conns before they were allocated ("assignment to entry in nil
// map"), which killed the plugin process on the first successful connection.
func TestNewHandlerInitializesMaps(t *testing.T) {
	h := newHandler(nilMapDialect{})
	if h.conns == nil {
		t.Fatal("conns map is nil")
	}
	if h.server == nil {
		t.Fatal("server map is nil")
	}
	// Writing must not panic.
	h.mu.Lock()
	h.conns["c1"] = nil
	h.server["c1"] = "x"
	h.mu.Unlock()
}

// sqliteTxDialect opens a single-connection in-memory SQLite database with a
// test table, so transaction routing can be exercised against a real engine.
type sqliteTxDialect struct{}

func (sqliteTxDialect) Code() string        { return "sqlitetest" }
func (sqliteTxDialect) Name() string        { return "SQLite Test" }
func (sqliteTxDialect) Version() string     { return "0" }
func (sqliteTxDialect) DefaultPort() string { return "0" }

func (sqliteTxDialect) Open(proto.ConnectParams) (*sql.DB, string, error) {
	h, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, "", err
	}
	// One connection keeps the in-memory database (and any transaction on it)
	// shared across every call the handler makes.
	h.SetMaxOpenConns(1)
	if _, err := h.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		h.Close()
		return nil, "", err
	}
	return h, "sqlite-test", nil
}
func (sqliteTxDialect) ListTables(*sql.DB) ([]proto.ObjectInfo, error) { return nil, nil }
func (sqliteTxDialect) ListColumns(*sql.DB, string) ([]proto.ColumnInfo, error) {
	return nil, nil
}
func (sqliteTxDialect) BrowseSQL(string, int) string                         { return "" }
func (sqliteTxDialect) CreateDatabase(*sql.DB, string, string, string) error { return nil }
func (sqliteTxDialect) QuoteIdent(name string) string                        { return `"` + name + `"` }
func (sqliteTxDialect) Placeholder(int) string                               { return "?" }
func (sqliteTxDialect) Metrics(*sql.DB) ([]proto.Metric, error)              { return nil, nil }

// countRows runs a COUNT(*) through the handler and returns it as a string.
func countRows(t *testing.T, h *handler, connID string) string {
	t.Helper()
	res, err := h.Query(proto.QueryParams{ConnID: connID, SQL: "SELECT COUNT(*) FROM t", MaxRows: 1})
	if err != nil {
		t.Fatalf("count query: %v", err)
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 1 || res.Rows[0][0] == nil {
		t.Fatalf("count rows = %+v", res.Rows)
	}
	return *res.Rows[0][0]
}

// TestHandlerTransactionRouting verifies that while a manual transaction is
// open, statements run inside it: a rollback discards the change and a commit
// persists it.
func TestHandlerTransactionRouting(t *testing.T) {
	h := newHandler(sqliteTxDialect{})
	open, err := h.Open(proto.ConnectParams{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := open.ConnID
	tx := proto.TxParams{ConnID: id}

	if got := countRows(t, h, id); got != "0" {
		t.Fatalf("initial count = %q, want 0", got)
	}

	// A second Begin without an intervening end fails.
	if err := h.Begin(tx); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := h.Begin(tx); err == nil {
		t.Fatal("second Begin should fail")
	}

	// Insert inside the transaction, then roll it back: the row must vanish.
	if _, err := h.Insert(proto.DMLSingleParams{ConnID: id, Table: "t", Values: map[string]*string{"v": sp("a")}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if got := countRows(t, h, id); got != "1" {
		t.Fatalf("count inside tx = %q, want 1", got)
	}
	if err := h.Rollback(tx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := countRows(t, h, id); got != "0" {
		t.Fatalf("count after rollback = %q, want 0", got)
	}

	// Commit persists the change.
	if err := h.Begin(tx); err != nil {
		t.Fatalf("Begin (2): %v", err)
	}
	if _, err := h.Insert(proto.DMLSingleParams{ConnID: id, Table: "t", Values: map[string]*string{"v": sp("b")}}); err != nil {
		t.Fatalf("Insert (2): %v", err)
	}
	if err := h.Commit(tx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := countRows(t, h, id); got != "1" {
		t.Fatalf("count after commit = %q, want 1", got)
	}

	// Commit/Rollback with no open transaction error.
	if err := h.Commit(tx); err == nil {
		t.Fatal("Commit with no tx should fail")
	}
	if err := h.Rollback(tx); err == nil {
		t.Fatal("Rollback with no tx should fail")
	}
}

// TestHandlerQueryStream verifies the plugin streams rows in batches (bounded
// memory) and that streaming inside a manual transaction sees uncommitted data.
func TestHandlerQueryStream(t *testing.T) {
	h := newHandler(sqliteTxDialect{})
	open, err := h.Open(proto.ConnectParams{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := open.ConnID

	// Seed 5 rows (autocommit).
	for i := 0; i < 5; i++ {
		if _, err := h.Insert(proto.DMLSingleParams{ConnID: id, Table: "t", Values: map[string]*string{"v": sp("x")}}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	// Stream with batch=2: expect 3 frames (2+2+1) and 5 rows total.
	var total int64
	var frames int
	var cols []proto.ColumnInfo
	res, err := h.QueryStream(proto.QueryStreamParams{ConnID: id, SQL: "SELECT id, v FROM t", Batch: 2},
		func(f proto.StreamFrame) error {
			frames++
			if len(f.Columns) > 0 {
				cols = f.Columns
			}
			total += int64(len(f.Rows))
			return nil
		})
	if err != nil {
		t.Fatalf("QueryStream: %v", err)
	}
	if total != 5 {
		t.Fatalf("streamed %d rows, want 5", total)
	}
	if frames != 3 {
		t.Fatalf("got %d frames, want 3 (batch=2 over 5 rows)", frames)
	}
	if len(cols) != 2 || cols[0].Name != "id" {
		t.Fatalf("columns = %+v", cols)
	}
	if res.RowsAffected != 0 {
		t.Fatalf("terminal RowsAffected = %d, want 0 for a SELECT", res.RowsAffected)
	}

	// Streaming inside a transaction sees the uncommitted insert.
	if err := h.Begin(proto.TxParams{ConnID: id}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := h.Insert(proto.DMLSingleParams{ConnID: id, Table: "t", Values: map[string]*string{"v": sp("y")}}); err != nil {
		t.Fatalf("Insert in tx: %v", err)
	}
	var inTx int64
	if _, err := h.QueryStream(proto.QueryStreamParams{ConnID: id, SQL: "SELECT id FROM t", Batch: 100},
		func(f proto.StreamFrame) error {
			inTx += int64(len(f.Rows))
			return nil
		}); err != nil {
		t.Fatalf("QueryStream in tx: %v", err)
	}
	if inTx != 6 {
		t.Fatalf("in-tx stream saw %d rows, want 6", inTx)
	}
	_ = h.Rollback(proto.TxParams{ConnID: id})

	// After rollback the streamed count is back to 5.
	var after int64
	if _, err := h.QueryStream(proto.QueryStreamParams{ConnID: id, SQL: "SELECT id FROM t", Batch: 100},
		func(f proto.StreamFrame) error {
			after += int64(len(f.Rows))
			return nil
		}); err != nil {
		t.Fatalf("QueryStream after rollback: %v", err)
	}
	if after != 5 {
		t.Fatalf("post-rollback stream saw %d rows, want 5", after)
	}
}

// TestHandlerMetrics verifies the plugin returns its dialect metrics.
func TestHandlerMetrics(t *testing.T) {
	h := newHandler(sqliteTxDialect{})
	open, err := h.Open(proto.ConnectParams{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := h.Metrics(proto.MetricsParams{ConnID: open.ConnID})
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	// sqliteTxDialect.Metrics returns nil (no metrics) — just ensure the
	// handler routes the call without error and yields an empty list.
	if len(res.Metrics) != 0 {
		t.Fatalf("metrics = %+v, want empty", res.Metrics)
	}

	// Unknown connection surfaces an error.
	if _, err := h.Metrics(proto.MetricsParams{ConnID: "nope"}); err == nil {
		t.Fatal("expected error for unknown connection")
	}
}

// TestKVMetricsSQLitePragma validates the exact query the sqlite plugin uses
// against a real in-memory database (pragma table-valued functions).
func TestKVMetricsSQLitePragma(t *testing.T) {
	h, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	const q = `
		SELECT 'Page count', CAST(page_count AS TEXT) FROM pragma_page_count()
		UNION ALL SELECT 'Page size', CAST(page_size AS TEXT) FROM pragma_page_size()
		UNION ALL SELECT 'Freelist pages', CAST(freelist_count AS TEXT) FROM pragma_freelist_count()`
	ms, err := KVMetrics(h, "SQLite", q)
	if err != nil {
		t.Fatalf("KVMetrics: %v", err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d metrics, want 3: %+v", len(ms), ms)
	}
	for _, m := range ms {
		if m.Value == "" {
			t.Fatalf("metric %q has empty value", m.Label)
		}
	}
}
