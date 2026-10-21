package drivercore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// sqliteDriver is the fake Driver the round-trip test serves: it opens a
// *sql.DB over a file, exactly like a real plugin would, so the whole framed
// protocol is exercised without spawning a subprocess.
type sqliteDriver struct{}

func (sqliteDriver) Open(cfg ConnConfig) (*sql.DB, error) {
	return sql.Open("sqlite3", cfg.Database)
}

// newPipeEngine wires a pluginEngine client to an in-process Serve loop over an
// in-memory pipe, sharing one sqlite file.
func newPipeEngine(t *testing.T, dbPath string) (Engine, func()) {
	t.Helper()
	client, server := net.Pipe()
	go func() { _ = Serve(sqliteDriver{}, server, server) }()
	proc := newPluginProcessConn(client, client, client)
	var res openResult
	if err := proc.call(methodOpen, openParams{Config: ConnConfig{Type: "sqlite", Database: dbPath}}, &res); err != nil {
		t.Fatalf("open over pipe: %v", err)
	}
	eng := &pluginEngine{proc: proc, session: res.Session}
	return eng, func() { eng.Close() }
}

// TestPluginRoundTrip drives the full protocol against a real engine over the
// wire: exec, query (with column names/types and typed values), and a
// commit-then-rollback transaction pair.
func TestPluginRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "rt.sqlite")
	eng, cleanup := newPipeEngine(t, dbPath)
	defer cleanup()
	ctx := context.Background()

	if _, err := eng.Exec(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, w REAL)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := eng.Exec(ctx, "INSERT INTO t (id, name, w) VALUES (1, 'a', 1.5), (2, NULL, 2.0)")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if res.Affected != 2 {
		t.Errorf("affected = %d, want 2", res.Affected)
	}

	rs, err := eng.Query(ctx, "SELECT id, name, w FROM t ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rs.Close()
	if got := rs.Columns(); len(got) != 3 || got[0] != "id" {
		t.Errorf("columns = %v, want [id name w]", got)
	}
	var (
		ids  []int64
		name []string
		wt   []float64
	)
	for rs.Next() {
		var id int64
		var nm string
		var w float64
		if err := rs.Scan(&id, &nm, &w); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
		name = append(name, nm)
		wt = append(wt, w)
	}
	if err := rs.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("ids = %v, want [1 2]", ids)
	}
	if name[1] != "" {
		t.Errorf("NULL name scanned as %q, want empty", name[1])
	}
	if wt[0] != 1.5 || wt[1] != 2.0 {
		t.Errorf("weights = %v, want [1.5 2]", wt)
	}

	// Commit path: a transaction's insert survives.
	tx, err := eng.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO t (id, name) VALUES (3, 'c')"); err != nil {
		t.Fatalf("tx exec: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	assertCount(t, eng, 3)

	// Rollback path: a transaction's insert does not survive.
	tx2, err := eng.Begin(ctx)
	if err != nil {
		t.Fatalf("begin2: %v", err)
	}
	if _, err := tx2.Exec(ctx, "INSERT INTO t (id, name) VALUES (4, 'd')"); err != nil {
		t.Fatalf("tx2 exec: %v", err)
	}
	if err := tx2.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	assertCount(t, eng, 3)
}

func assertCount(t *testing.T, eng Engine, want int) {
	t.Helper()
	rs, err := eng.Query(context.Background(), "SELECT COUNT(*) FROM t")
	if err != nil {
		t.Fatalf("count query: %v", err)
	}
	defer rs.Close()
	var n int
	if rs.Next() {
		if err := rs.Scan(&n); err != nil {
			t.Fatalf("count scan: %v", err)
		}
	}
	if n != want {
		t.Errorf("row count = %d, want %d", n, want)
	}
}

// TestCodecRoundTrip pins the tagged-value codec: the value shapes a plugin
// ships (nil, int64, float64, bool, string, []byte, time) survive the encode /
// decode cycle and scan into the right Go types.
func TestCodecRoundTrip(t *testing.T) {
	orig := []interface{}{int64(7), 3.25, true, "hi", []byte("bytes")}
	encoded := encodeRow(orig)
	decoded := decodeRow(encoded)
	if len(decoded) != len(orig) {
		t.Fatalf("decoded %d values, want %d", len(decoded), len(orig))
	}
	if decoded[0] != int64(7) {
		t.Errorf("int64 became %T %v", decoded[0], decoded[0])
	}
	if decoded[1] != 3.25 {
		t.Errorf("float64 became %T %v", decoded[1], decoded[1])
	}
	if decoded[2] != true {
		t.Errorf("bool became %T %v", decoded[2], decoded[2])
	}
	if decoded[3] != "hi" {
		t.Errorf("string became %T %v", decoded[3], decoded[3])
	}
	// []byte is shipped as a string (the display shape), so it comes back a
	// string, not []byte.
	if decoded[4] != "bytes" {
		t.Errorf("[]byte became %T %v", decoded[4], decoded[4])
	}
}

// TestConvertAssignNumeric checks the coercion a materialized row leans on: a
// JSON number that arrived as float64 must still scan into an int64 column.
func TestConvertAssignNumeric(t *testing.T) {
	var i int64
	if err := convertAssign(&i, float64(42)); err != nil {
		t.Fatalf("float64->int64: %v", err)
	}
	if i != 42 {
		t.Errorf("got %d, want 42", i)
	}
	var s string
	if err := convertAssign(&s, int64(9)); err != nil {
		t.Fatalf("int64->string: %v", err)
	}
	if s != "9" {
		t.Errorf("got %q, want 9", s)
	}
	var f float64
	if err := convertAssign(&f, int64(5)); err != nil {
		t.Fatalf("int64->float64: %v", err)
	}
	if f != 5 {
		t.Errorf("got %v, want 5", f)
	}
}

// ---- cancellation ----

// slowConn is a database/sql connection whose query blocks until its context is
// cancelled - a stand-in for a long-running statement on a real server.
type slowConn struct{}

func (slowConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("prepare unused") }
func (slowConn) Close() error                        { return nil }
func (slowConn) Begin() (driver.Tx, error)           { return nil, errors.New("begin unused") }
func (slowConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type slowDriver struct{}

func (slowDriver) Open(string) (driver.Conn, error) { return slowConn{}, nil }

// cancelFakeDriver is a Driver whose Open hands back a pool over slowConn.
type cancelFakeDriver struct{}

func (cancelFakeDriver) Open(cfg ConnConfig) (*sql.DB, error) {
	return sql.Open("slowcancel", "")
}

var registerSlow = sync.OnceFunc(func() { sql.Register("slowcancel", slowDriver{}) })

// TestPluginCancelsQuery proves the cross-process cancellation path: a query
// that blocks until its context is cancelled, driven over the wire, must return
// promptly with an error once the app cancels - the plugin trips the statement's
// context on the "cancel" request rather than running it to completion.
func TestPluginCancelsQuery(t *testing.T) {
	registerSlow()
	client, server := net.Pipe()
	go func() { _ = Serve(cancelFakeDriver{}, server, server) }()
	proc := newPluginProcessConn(client, client, client)
	var res openResult
	if err := proc.call(methodOpen, openParams{Config: ConnConfig{Type: "x"}}, &res); err != nil {
		t.Fatalf("open: %v", err)
	}
	eng := &pluginEngine{proc: proc, session: res.Session}
	defer eng.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := eng.Query(ctx, "SELECT that_blocks"); err == nil {
		t.Fatal("expected the cancelled query to return an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation did not return promptly: %v", elapsed)
	}
}

// TestPluginStreamsManyRows crosses the chunk boundary (queryChunkRows = 500):
// a result of 1200 rows travels as head + 2 full chunks + a partial + done, and
// the client must reassemble every row in order.
func TestPluginStreamsManyRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "many.sqlite")
	eng, cleanup := newPipeEngine(t, dbPath)
	defer cleanup()
	ctx := context.Background()

	if _, err := eng.Exec(ctx, "CREATE TABLE m (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	seed := `INSERT INTO m (id, v) WITH RECURSIVE c(x) AS (
		SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 1200) SELECT x, 'r'||x FROM c`
	if _, err := eng.Exec(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rs, err := eng.Query(ctx, "SELECT id, v FROM m ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rs.Close()
	var (
		n    int
		last int64
	)
	for rs.Next() {
		var id int64
		var v string
		if err := rs.Scan(&id, &v); err != nil {
			t.Fatalf("scan row %d: %v", n, err)
		}
		if id != int64(n+1) {
			t.Fatalf("row %d id = %d, want %d (order/assembly broken)", n, id, n+1)
		}
		last = id
		n++
	}
	if err := rs.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if n != 1200 {
		t.Fatalf("got %d rows, want 1200 (chunked stream lost rows)", n)
	}
	if last != 1200 {
		t.Errorf("last id = %d, want 1200", last)
	}
}
