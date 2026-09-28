package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// TestEndToEndSQLite drives the full core pipeline against a real SQLite file:
// connect, execute SELECT, and browse databases / tables / columns. This proves
// the DSN, pooling and query paths all work together (no GUI required).
func TestEndToEndSQLite(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	sdb, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Seed a table and some rows directly.
	if _, err := sdb.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, active INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := sdb.Exec(`INSERT INTO users (name, active) VALUES ('alice', 1), ('bob', 0)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	sdb.Close()

	c := &Client{store: db.NewStore()}
	cfg := db.ConnConfig{ID: "c0", Type: db.DbTypeSQLite, Database: dbFile}
	if err := c.connect(cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect(cfg.ID) })
	conn, err := c.store.OpenConn(cfg.ID)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}

	// Execute a SELECT.
	res := c.store.Execute(context.Background(), conn, "", "SELECT id, name, active FROM users ORDER BY id", 100)
	if res.Error != "" {
		t.Fatalf("execute: %s", res.Error)
	}
	if res.RowCount != 2 {
		t.Fatalf("expected 2 rows, got %d", res.RowCount)
	}
	if len(res.Columns) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(res.Columns))
	}
	if res.Rows[0][1] != "alice" {
		t.Fatalf("expected first row name alice, got %v", res.Rows[0][1])
	}

	// Row limit wrapper applies to plain SELECTs.
	limited := c.store.Execute(context.Background(), conn, "", "SELECT * FROM users", 1)
	if limited.RowCount != 1 {
		t.Fatalf("row limit: expected 1 row, got %d", limited.RowCount)
	}

	// Non-SELECT command reports affected rows.
	ins := c.store.Execute(context.Background(), conn, "", "INSERT INTO users (name, active) VALUES ('carol', 1)", 0)
	if ins.Error != "" {
		t.Fatalf("insert exec: %s", ins.Error)
	}
	if ins.AffectedRows != 1 {
		t.Fatalf("expected 1 affected row, got %d", ins.AffectedRows)
	}

	// Schema browsing.
	dbs, err := c.store.Databases(cfg.ID)
	if err != nil || len(dbs) != 1 {
		t.Fatalf("databases: %v (len=%d)", err, len(dbs))
	}
	if dbs[0] != dbFile {
		t.Fatalf("expected database %q, got %q", dbFile, dbs[0])
	}

	tables, err := c.store.Tables(cfg.ID, dbFile, "")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "users" {
		t.Fatalf("expected users table, got %+v", tables)
	}

	cols, err := c.store.Columns(cfg.ID, dbFile, "", "users")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(cols))
	}
	var sawPK bool
	for _, col := range cols {
		if col.Name == "id" && col.IsPrimaryKey {
			sawPK = true
		}
	}
	if !sawPK {
		t.Fatalf("expected id to be a primary key, got %+v", cols)
	}
}

// TestQueryCancel proves the cancellation plumbing: an already-cancelled
// context makes execute report a cancellation error, which is what the
// cancel button produces for a long-running query.
func TestQueryCancel(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("")
	c := newTestClient(a, w)
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "t.db")

	sdb, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sdb.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := sdb.Exec(`INSERT INTO users (name) VALUES ('a')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sdb.Close()

	if err := c.connect(db.ConnConfig{ID: "c0", Name: "t", Type: db.DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })

	conn, err := c.store.OpenConn("c0")
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := c.store.Execute(ctx, conn, "", "SELECT name FROM users", 0)
	if res.Error == "" {
		t.Fatal("expected a cancellation error, got none")
	}
}

// TestForeignKeyMemoAndPicker drives the client-side FK path against a real
// SQLite file: the constraint list is read once and memoized for the session,
// and the picker turns that constraint into the values a cell may hold.
func TestForeignKeyMemoAndPicker(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "sample.db")

	sdb, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO customers (id, name) VALUES (2, 'Bob'), (1, 'Alice')`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id))`,
	} {
		if _, err := sdb.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	sdb.Close()

	c := &Client{store: db.NewStore(), fkCache: make(map[string][]db.ForeignKeyInfo)}
	if err := c.connect(db.ConnConfig{ID: "c0", Name: "t", Type: db.DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })
	conn, err := c.store.OpenConn("c0")
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}

	fks, err := c.foreignKeys("c0", dbFile, "", "orders")
	if err != nil {
		t.Fatalf("foreignKeys: %v", err)
	}
	if len(fks) != 1 || fks[0].RefTable != "customers" || fks[0].Columns[0] != "customer_id" {
		t.Fatalf("foreignKeys = %+v", fks)
	}

	// The catalog is now behind the memo: a schema change the session cannot
	// see must not change the answer, because nothing re-reads it.
	if _, err := conn.DB.Exec(`DROP TABLE orders`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	twoKeys := `CREATE TABLE orders (id INTEGER PRIMARY KEY,
		customer_id INTEGER REFERENCES customers(id),
		biller_id INTEGER REFERENCES customers(id))`
	if _, err := conn.DB.Exec(twoKeys); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if again, err := c.foreignKeys("c0", dbFile, "", "orders"); err != nil {
		t.Fatalf("second foreignKeys: %v", err)
	} else if len(again) != 1 {
		t.Fatalf("expected the memoized constraint list, got %+v", again)
	}

	// A different table is a different cache entry, not the same answer.
	if got, err := c.foreignKeys("c0", dbFile, "", "customers"); err != nil || len(got) != 0 {
		t.Fatalf("customers has no foreign keys, got %+v (%v)", got, err)
	}

	// The picker reads the referenced table through the dialect's own name
	// builder, so "what could this cell hold" is the key set the write would
	// satisfy. Ordering on the key makes the list stable.
	cands, err := c.foreignKeyValues("c0", dbFile, "", "orders", "customer_id", 10)
	if err != nil {
		t.Fatalf("foreignKeyValues: %v", err)
	}
	if cands.RefTable != "customers" || cands.RefColumn != "id" {
		t.Fatalf("candidates target = %s.%s", cands.RefTable, cands.RefColumn)
	}
	if len(cands.Values) != 2 || cands.Values[0].Value != "1" || cands.Values[1].Value != "2" {
		t.Fatalf("candidate values = %+v", cands.Values)
	}
	if cands.LabelColumn != "name" || cands.Values[0].Label != "Alice" {
		t.Fatalf("candidate labels = %q %+v", cands.LabelColumn, cands.Values)
	}
	if cands.Truncated {
		t.Error("2 rows under a limit of 10 is not a truncated list")
	}

	// The composite-key refusal is what keeps such a cell plain text.
	if _, err := c.foreignKeyValues("c0", dbFile, "", "orders", "biller_id", 10); err == nil {
		t.Error("a memoized constraint list that lacks this column must be refused")
	}
}

// TestQueryGoroutineUsesSessionSnapshot pins the F2 invariant: the work
// goroutine of a running query must not reach for the session fields. The
// query is deliberately slow (a million-row recursive scan), so the whole
// window in which a mid-flight 连接/断开 or 每页 change lands is open: a
// mutator rewrites c.currentDB under c.mu for as long as the query runs.
// Under -race the old unlocked read shows up as a data race; the recorded
// history entry shows up as filed under the decoy database instead of the
// one the run started on.
func TestQueryGoroutineUsesSessionSnapshot(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "t.db")
	decoy := filepath.Join(dir, "other.db")

	sdb, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sdb.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sdb.Close()

	if err := c.connect(db.ConnConfig{ID: "c0", Name: "t", Type: db.DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.disconnect("c0") })
	w.SetContent(c.buildUI())
	c.store.ReplaceHistory(nil)

	// Async mode, with the finish callback parked on a channel: the test owns
	// the moment the UI thread consumes it, so nothing below depends on how
	// long the query takes.
	c.asyncRun = true
	finished := make(chan func(), 1)
	c.dispatch = func(f func()) { finished <- f }

	c.queryEntry.setTextSilently(`WITH RECURSIVE cnt(x) AS (
		SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < 300000
	) SELECT count(*) FROM cnt`)
	c.runCurrent()

	// runCurrent has returned, so its session snapshot is taken; from here the
	// query is in flight and the session is fair game for the UI thread.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			c.mu.Lock()
			c.currentDB = decoy
			c.mu.Unlock()
			time.Sleep(time.Millisecond)
			c.mu.Lock()
			c.currentDB = dbFile
			c.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	finish := <-finished
	close(stop)
	wg.Wait()
	// Put the session back before the callback runs; the callback itself reads
	// it on the UI thread, which is allowed.
	c.mu.Lock()
	c.currentDB = dbFile
	c.mu.Unlock()
	finish()

	h := c.store.History()
	if len(h) != 1 {
		t.Fatalf("history entries = %d, want 1", len(h))
	}
	if h[0].ConnID != "c0" || h[0].Database != dbFile {
		t.Fatalf("history filed under %q/%q, want the session snapshot c0/%q",
			h[0].ConnID, h[0].Database, dbFile)
	}
	if h[0].Error != "" {
		t.Fatalf("query failed: %s", h[0].Error)
	}
}
