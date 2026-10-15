package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// The catalog readers used to have no context at all: collectRows called
// db.Query, so a catalog read on a remote server could only end when the server
// decided to answer. That is the second half of the freeze - a 取消 button whose
// context reaches the query path but not the catalog path stops nothing - and it
// is what the two tests below pin.

// TestCatalogReadRefusesACanceledContext says a read that is born canceled never
// reaches the server: the error is the caller's, not an empty result the grid
// would paint as "no columns".
func TestCatalogReadRefusesACanceledContext(t *testing.T) {
	file := seedSQLiteFile(t)
	c := &Store{live: make(map[string]*Conn)}
	cfg := ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: file}
	if err := c.Connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect("c0") })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cols, err := c.Columns(ctx, "c0", file, "", "t")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Columns on a canceled context = %v (%d columns), want context.Canceled", err, len(cols))
	}
	// The same reader reached through the dialect table, because every engine's
	// catalog closure shares collectRows: one of them ignoring the context is
	// enough to hang a window.
	conn := c.live["c0"]
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		t.Fatalf("dialectFor: %v", err)
	}
	if _, err := d.columns(ctx, c, conn, file, "", "t"); !errors.Is(err, context.Canceled) {
		t.Errorf("the dialect reader ignored the canceled context: %v", err)
	}
}

// TestCatalogReadUnblocksWhenTheCallerGivesUp is the half a pre-canceled context
// cannot prove: a read that is already waiting stops waiting. The pool is capped
// at one connection and the only one is held by an unfinished scan, so the
// catalog read cannot start at all. A reader that calls db.Query sits there
// until the connection comes free, which on a remote server is the window that
// will not draw; a reader that hands the driver its context comes back with the
// caller's verdict instead.
func TestCatalogReadUnblocksWhenTheCallerGivesUp(t *testing.T) {
	file := seedSQLiteFile(t)
	c := &Store{live: make(map[string]*Conn)}
	cfg := ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: file}
	if err := c.Connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect("c0") })

	conn := c.live["c0"]
	handle, err := conn.handle()
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	handle.SetMaxOpenConns(1)

	held, err := handle.Query("SELECT * FROM t")
	if err != nil {
		t.Fatalf("holding the only connection: %v", err)
	}
	t.Cleanup(func() { held.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	cols, err := c.Columns(ctx, "c0", file, "", "t")
	waited := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Columns with no connection to borrow = %v (%d columns), want the deadline error", err, len(cols))
	}
	// The bound is deliberately loose: this asserts that the read gave up when
	// its deadline passed rather than staying parked on the connection.
	if waited > 5*time.Second {
		t.Errorf("the read kept waiting after its deadline: %v", waited)
	}
}

// TestSearchWalkStopsWhenGivenUpOn covers the one reader that is not a single
// query: the quick-open walk runs a catalog query per database, so it has to
// notice the caller stopped caring between two of them.
func TestSearchWalkStopsWhenGivenUpOn(t *testing.T) {
	file := seedSQLiteFile(t)
	c := &Store{live: make(map[string]*Conn)}
	cfg := ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: file}
	if err := c.Connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect("c0") })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The live connection is enough for the walk to have work to do; giving up
	// before the first database is still a cancellation, not an empty answer.
	hits, err := c.SearchObjects(ctx, "t", 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("SearchObjects on a canceled context = %v (%d hits), want context.Canceled", err, len(hits))
	}
}

// TestAbandonedSessionIsNotRegistered is ⑥ meeting ④: a per-database session the
// caller gave up on while it was being opened must be closed again, not hung on
// the Conn for the next reader to reuse. The pool is opened on a fresh file so
// the ping is the only thing left to do, and the ping is what the context stops.
func TestAbandonedSessionIsNotRegistered(t *testing.T) {
	file := filepath.Join(t.TempDir(), "poolctx.db")
	c := &Store{live: make(map[string]*Conn)}
	cfg := ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: seedSQLiteFile(t)}
	if err := c.Connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect("c0") })

	conn := c.live["c0"]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.perDatabasePool(ctx, conn, file); err == nil {
		t.Fatal("a session whose caller had given up opened anyway")
	}
	conn.mu.Lock()
	open := len(conn.pools)
	conn.mu.Unlock()
	if open != 0 {
		t.Errorf("the abandoned session stayed registered: %d pools on a conn that never used it", open)
	}
}
