package db

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// seedSQLiteFile creates a SQLite file with one table and closes it, so the
// tests below can connect to it through the real code path.
func seedSQLiteFile(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "lifecycle.db")
	db, err := createConnection(ConnConfig{Type: DbTypeSQLite, Database: file})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()
	return file
}

// TestCatalogReadSurvivesDisconnect pins the locking discipline of the live
// handle. Conn.DB was a plain exported field: Connect/Disconnect nilled it
// under Store.mu, while every catalog reader reached for it with no lock at all
// (poolFor, perDatabasePool and the dialect's collectRows call sites). A reader
// racing a Disconnect could dereference the nil handle inside db.Query and panic
// the process.
//
// The recover count is what turns a crash into a test failure rather than an
// aborted run. Honest caveat, measured with a negative control: this loop does
// NOT make -race report the unsynchronised pair, because both sides pass through
// Store.mu, which orders every read and write the loop actually performs. The
// invariant this file really pins is the deterministic one - the test below
// holds the *Conn across the Disconnect, which is where the old exported field
// could come back nil between two reads of the same statement.
func TestCatalogReadSurvivesDisconnect(t *testing.T) {
	file := seedSQLiteFile(t)
	cfg := ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: file}

	c := &Store{live: make(map[string]*Conn)}
	if err := c.Connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect("c0") })

	var panics int64
	read := func() {
		defer func() {
			if r := recover(); r != nil {
				atomic.AddInt64(&panics, 1)
			}
		}()
		// The error is deliberately ignored: after a Disconnect the honest
		// answer is "not connected", and that must be an error, not a crash.
		_, _ = c.Columns(context.Background(), "c0", file, "", "t")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 400; i++ {
			read()
		}
	}()
	for i := 0; i < 400; i++ {
		if err := c.Connect(cfg); err != nil {
			t.Fatalf("reconnect %d: %v", i, err)
		}
		c.Disconnect("c0")
	}
	<-done

	if n := atomic.LoadInt64(&panics); n != 0 {
		t.Errorf("%d catalog reads dereferenced a handle that Disconnect had just nilled", n)
	}
}

// TestDialectReadAfterDisconnectIsAnError is the same race as above written out
// as the one interleaving that matters, so the check does not depend on winning
// a microsecond-wide window: the reader already holds the *Conn when Disconnect
// goes through, and the dialect then reaches for a handle nobody has any more.
// While Conn.DB was a plain field that read was a nil dereference inside
// db.Query - a panicked process - and the honest answer is the error.
func TestDialectReadAfterDisconnectIsAnError(t *testing.T) {
	file := seedSQLiteFile(t)
	c := &Store{live: make(map[string]*Conn)}
	cfg := ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: file}
	if err := c.Connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	conn := c.live["c0"]
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		t.Fatalf("dialectFor: %v", err)
	}
	c.Disconnect("c0")

	// read turns the dialect call into an error even when it panics, so a nil
	// dereference is reported as the failure it is instead of killing the run.
	read := func(name string, run func() error) {
		t.Helper()
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panicked on the detached handle: %v", r)
				}
			}()
			return run()
		}()
		if err == nil || err.Error() != "not connected" {
			t.Errorf("%s on a disconnected conn = %v, want the not-connected error", name, err)
		}
	}
	read("columns", func() error {
		_, err := d.columns(context.Background(), c, conn, file, "", "t")
		return err
	})
	read("indexes", func() error {
		_, err := d.indexes(context.Background(), c, conn, file, "", "t")
		return err
	})
	read("tables", func() error {
		_, err := d.tables(context.Background(), c, conn, file, "")
		return err
	})
}

// TestClosedConnCannotOpenNewPools is the session-leak half. DeleteConnection
// used to close the pools and only then drop the *Conn from Store.live, so an
// in-flight query still holding that *Conn could walk back in through
// perDatabasePool, open a fresh *sql.DB and hang it on an object nobody keeps
// a reference to any more - nothing ever closes it, and the server counts that
// session as live. A closed Conn must refuse to open anything, whatever the
// caller still holds.
func TestClosedConnCannotOpenNewPools(t *testing.T) {
	file := seedSQLiteFile(t)
	c := &Store{live: make(map[string]*Conn)}
	cfg := ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: file}
	if err := c.Connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	conn := c.live["c0"]
	c.Disconnect("c0")

	// SQLite is the engine this suite can open without dialling a server, which
	// is what makes the leak observable: the call below would otherwise really
	// create and register a pool on a Conn that is already closed.
	other, err := c.perDatabasePool(context.Background(), conn, filepath.Join(t.TempDir(), "leaked.db"))
	if err == nil {
		other.Close()
		t.Error("a closed connection opened a new pool; nothing can close it again")
	}

	conn.mu.Lock()
	open := len(conn.pools)
	conn.mu.Unlock()
	if open != 0 {
		t.Errorf("closed connection holds %d pools after it was disconnected", open)
	}
}

// TestReadersAfterDisconnectSayNotConnected is the invariant every reader now
// depends on: the handle is gone, so the answer is this error rather than a
// zero-value result the grid would paint as an empty table.
func TestReadersAfterDisconnectSayNotConnected(t *testing.T) {
	file := seedSQLiteFile(t)
	c := &Store{live: make(map[string]*Conn)}
	cfg := ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: file}
	if err := c.Connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	conn := c.live["c0"]
	if _, err := c.poolFor(context.Background(), conn, ""); err != nil {
		t.Fatalf("while live, poolFor must hand out the handle: %v", err)
	}
	c.Disconnect("c0")

	if _, err := c.poolFor(context.Background(), conn, ""); err == nil || err.Error() != "not connected" {
		t.Errorf("poolFor after Disconnect = %v, want the not-connected error", err)
	}
	if _, err := c.OpenConn("c0"); err == nil || err.Error() != "not connected" {
		t.Errorf("OpenConn after Disconnect = %v, want the not-connected error", err)
	}
	if c.ConnectionStatus("c0") {
		t.Error("ConnectionStatus still reports the connection live")
	}
}
