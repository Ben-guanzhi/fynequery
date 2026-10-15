package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// ApplyChanges is the one place this package writes to a server, and its promise
// is all-or-nothing: the pure builder's statement text is covered in
// changes_test.go, but "either every edit lands or none does" can only be proven
// against a transaction that really rolls back. SQLite is that transaction and
// needs no server, so the whole matrix below runs everywhere.

const changeConnID = "tx"

// itemsSeed is the table the matrix writes against: an INTEGER key the grid can
// leave out of an INSERT, a NOT NULL column a bad edit can violate, and a UNIQUE
// column a colliding INSERT can violate.
const itemsSeed = `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, note TEXT UNIQUE)`

const itemsRows = `INSERT INTO items (id, name, note) VALUES (1, 'alpha', 'n1'), (2, 'beta', 'n2'), (3, 'gamma', 'n3')`

// newChangeStore connects a SQLite file and runs the statements given, which is
// how every case below starts from a known table.
func newChangeStore(t *testing.T, statements ...string) (*Store, string) {
	t.Helper()
	// The fixture must not touch anyone's saved connections or history.
	t.Setenv("TABLEPRO_STATE_DIR", t.TempDir())

	file := filepath.Join(t.TempDir(), "changes.db")
	store := NewStore()
	t.Cleanup(store.CloseAll)
	if err := store.Connect(ConnConfig{
		ID: changeConnID, Name: "changes", Type: DbTypeSQLite, Database: file,
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	conn, err := store.OpenConn(changeConnID)
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	for _, stmt := range statements {
		if res := store.Execute(context.Background(), conn, file, stmt, 0); res.Error != "" {
			t.Fatalf("seed %q: %s", stmt, res.Error)
		}
	}
	return store, file
}

// dumpTable renders a table one row per line, first column first, so a rollback
// failure reads as the rows that should not be there rather than as a count.
func dumpTable(t *testing.T, store *Store, file, table string) string {
	t.Helper()
	conn, err := store.OpenConn(changeConnID)
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	res := store.Execute(context.Background(), conn, file, "SELECT * FROM "+table+" ORDER BY 1", 0)
	if res.Error != "" {
		t.Fatalf("read back: %s", res.Error)
	}
	lines := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		cells := make([]string, 0, len(row))
		for _, value := range row {
			cells = append(cells, ValueString(value))
		}
		lines = append(lines, strings.Join(cells, "|"))
	}
	return strings.Join(lines, "\n")
}

// itemSet points a queue of grid edits at the items table, with the columns the
// result showed - the same shape the grid hands the store.
func itemSet(file string, changes ...RowChange) ChangeSet {
	return ChangeSet{
		ConnID: changeConnID, Database: file, Table: "items",
		Columns: []string{"id", "name", "note"}, Changes: changes,
	}
}

// TestApplyChangesCommitsTheWholeBatch is the success path all three kinds share
// one transaction: the update, the delete and the insert are all visible
// together, and the count is the rows the batch wrote.
func TestApplyChangesCommitsTheWholeBatch(t *testing.T) {
	store, file := newChangeStore(t, itemsSeed, itemsRows)

	applied, err := store.ApplyChanges(itemSet(file,
		RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
			Edits: []CellEdit{{Column: "name", Value: "alpha2"}}},
		RowChange{Kind: RowDelete, Original: []interface{}{int64(2), "beta", "n2"}},
		RowChange{Kind: RowInsert, Edits: []CellEdit{
			{Column: "name", Value: "delta"}, {Column: "note", Value: "n4"}}},
	))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied != 3 {
		t.Errorf("applied = %d, want one per row the batch wrote", applied)
	}
	want := "1|alpha2|n1\n3|gamma|n3\n4|delta|n4"
	if got := dumpTable(t, store, file, "items"); got != want {
		t.Errorf("table after commit =\n%s\nwant\n%s", got, want)
	}
}

// TestApplyChangesWritesBackAReadRow is the matrix's premise: the grid keeps
// the row exactly as the result read produced it and hands it over as the
// original (collectChanges in ui_grid_edit.go), so the builder has to accept
// what a read actually yields rather than what a test finds convenient to
// write. An INTEGER primary key reads back as int64, and a key predicate that
// refuses int64 fails every UPDATE and DELETE the grid can queue.
func TestApplyChangesWritesBackAReadRow(t *testing.T) {
	store, file := newChangeStore(t, itemsSeed, itemsRows)
	conn, err := store.OpenConn(changeConnID)
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	read := store.Execute(context.Background(), conn, file, "SELECT * FROM items ORDER BY 1", 0)
	if read.Error != "" {
		t.Fatalf("read: %s", read.Error)
	}
	// Name the type the rest of this test depends on: if a reader ever hands the
	// grid something else, the premise moved and this says so out loud.
	if _, ok := read.Rows[0][0].(int64); !ok {
		t.Fatalf("an INTEGER key reads back as %T, not int64", read.Rows[0][0])
	}

	applied, err := store.ApplyChanges(itemSet(file,
		RowChange{Original: read.Rows[0], Edits: []CellEdit{{Column: "name", Value: "alpha2"}}},
		RowChange{Kind: RowDelete, Original: read.Rows[1]},
	))
	if err != nil {
		t.Fatalf("apply a queue built from read rows: %v", err)
	}
	if applied != 2 {
		t.Errorf("applied = %d, want one per row the batch wrote", applied)
	}
	if got := dumpTable(t, store, file, "items"); got != "1|alpha2|n1\n3|gamma|n3" {
		t.Errorf("table =\n%s\nwant the read row updated and the next deleted", got)
	}

	// The same round trip past the point a float64 key stops being exact: an
	// int64 rendered through the float path would name a different row.
	store2, file2 := newChangeStore(t,
		`CREATE TABLE big (id INTEGER PRIMARY KEY, v TEXT)`,
		`INSERT INTO big (id, v) VALUES (9007199254740993, 'one'), (9007199254740994, 'two')`)
	conn2, err := store2.OpenConn(changeConnID)
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	big := store2.Execute(context.Background(), conn2, file2, "SELECT * FROM big ORDER BY 1", 0)
	if big.Error != "" {
		t.Fatalf("read: %s", big.Error)
	}
	set := ChangeSet{
		ConnID: changeConnID, Database: file2, Table: "big",
		Columns: []string{"id", "v"},
		Changes: []RowChange{{Original: big.Rows[1], Edits: []CellEdit{{Column: "v", Value: "changed"}}}},
	}
	statements, err := store2.BuildChanges(set)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(statements[0], "9007199254740994") {
		t.Errorf("statement = %s, want the key exact", statements[0])
	}
	if applied, err = store2.ApplyChanges(set); err != nil || applied != 1 {
		t.Fatalf("apply: applied %d, err %v", applied, err)
	}
	if got := dumpTable(t, store2, file2, "big"); got != "9007199254740993|one\n9007199254740994|changed" {
		t.Errorf("table =\n%s\nwant only the row the key names changed", got)
	}
}

// TestApplyChangesLocatesARowByKeyTypedAsText is the same cliff reached from the
// other side: the key did not come from a read, someone wrote it into the grid or
// the filter bar, so it arrives as text. Text went through ParseFloat before, and
// 9007199254740993 is not a double - the nearest one is 9007199254740992, a key
// this table does not have. That is not a wrong value written, it is the
// "matched no row" guard aborting a batch over a key the user spelled correctly,
// and on a table that did hold ...992 it would edit the neighbouring row instead.
func TestApplyChangesLocatesARowByKeyTypedAsText(t *testing.T) {
	store, file := newChangeStore(t,
		`CREATE TABLE big (id INTEGER PRIMARY KEY, v TEXT)`,
		`INSERT INTO big (id, v) VALUES (9007199254740993, 'one'), (9007199254740992, 'two')`)
	// The premise: the odd key really is stored, and its neighbour is a different
	// row that must not be touched.
	if got := dumpTable(t, store, file, "big"); !strings.Contains(got, "9007199254740993|one") {
		t.Fatalf("fixture =\n%s\nwant the odd key stored exactly", got)
	}

	set := ChangeSet{
		ConnID: changeConnID, Database: file, Table: "big",
		Columns: []string{"id", "v"},
		// id arrives as the text a cell holds, not as the int64 a read yielded.
		Changes: []RowChange{{Original: []interface{}{"9007199254740993", "one"},
			Edits: []CellEdit{{Column: "v", Value: "changed"}}}},
	}
	statements, err := store.BuildChanges(set)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(statements[0], "= 9007199254740993") {
		t.Errorf("statement = %s, want the typed key written exactly", statements[0])
	}
	applied, err := store.ApplyChanges(set)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want the one row the key names", applied)
	}
	if got := dumpTable(t, store, file, "big"); got != "9007199254740992|two\n9007199254740993|changed" {
		t.Errorf("table =\n%s\nwant the row the key names changed and its neighbour left alone", got)
	}
}

// TestApplyChangesRollsBackWhenAStatementFails is the promise: statement 1 had
// already written its row when statement 2 hit the UNIQUE constraint, and none
// of it may survive. A batch that ran without a transaction looks identical
// until this test is written.
func TestApplyChangesRollsBackWhenAStatementFails(t *testing.T) {
	store, file := newChangeStore(t, itemsSeed,
		`INSERT INTO items (id, name, note) VALUES (1, 'alpha', 'n1'), (2, 'beta', 'n2')`)

	applied, err := store.ApplyChanges(itemSet(file,
		RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
			Edits: []CellEdit{{Column: "name", Value: "renamed"}}},
		// note is UNIQUE and 'n2' is taken, so this is the statement that dies.
		RowChange{Kind: RowInsert, Edits: []CellEdit{
			{Column: "name", Value: "clash"}, {Column: "note", Value: "n2"}}},
	))
	if err == nil {
		t.Fatal("a constraint violation must fail the run, not report success")
	}
	if !strings.Contains(err.Error(), "statement 2") {
		t.Errorf("err = %v, want it to name the statement that failed", err)
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Errorf("err = %v, want the server's own reason", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0: a rolled back batch committed nothing", applied)
	}
	want := "1|alpha|n1\n2|beta|n2"
	if got := dumpTable(t, store, file, "items"); got != want {
		t.Errorf("table after rollback =\n%s\nwant the untouched seed", got)
	}
}

// TestApplyChangesAbortsWhenAStatementMatchesNoRow covers the silent-skipping
// failure the guard exists for: the row was deleted by someone else after the
// grid read it, so the second statement writes nothing. The first statement's
// write has to be rolled back with it, and the count has to say so too.
func TestApplyChangesAbortsWhenAStatementMatchesNoRow(t *testing.T) {
	store, file := newChangeStore(t, itemsSeed,
		`INSERT INTO items (id, name, note) VALUES (1, 'alpha', 'n1'), (2, 'beta', 'n2')`)

	applied, err := store.ApplyChanges(itemSet(file,
		RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
			Edits: []CellEdit{{Column: "name", Value: "renamed"}}},
		// id 99 is not in the table, and this is not an error the server raises.
		RowChange{Kind: RowDelete, Original: []interface{}{int64(99), "gone", "n9"}},
	))
	if err == nil {
		t.Fatal("a statement that matched no row must abort the batch")
	}
	if !strings.Contains(err.Error(), "matched no row") || !strings.Contains(err.Error(), "nothing was committed") {
		t.Errorf("err = %v, want both what happened and that nothing landed", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0: a rolled back batch committed nothing", applied)
	}
	want := "1|alpha|n1\n2|beta|n2"
	if got := dumpTable(t, store, file, "items"); got != want {
		t.Errorf("table after abort =\n%s\nwant the untouched seed", got)
	}

	// The same queue with the row still present must succeed, or the guard above
	// would pass for the wrong reason: a harness that never matches a row also
	// never commits one.
	applied, err = store.ApplyChanges(itemSet(file,
		RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
			Edits: []CellEdit{{Column: "name", Value: "renamed"}}},
		RowChange{Kind: RowDelete, Original: []interface{}{int64(2), "beta", "n2"}},
	))
	if err != nil || applied != 2 {
		t.Fatalf("same batch against rows that exist: applied %d, err %v", applied, err)
	}
	if got := dumpTable(t, store, file, "items"); got != "1|renamed|n1" {
		t.Errorf("table after the retry = %q, want both writes committed", got)
	}
}

// TestIdenticalValueUpdateCountsAsOneRow is the reference the MySQL DSN flag is
// set to match: re-saving what is already stored still reports a matched row, so
// the guard above reads "the row is gone" only when the row is gone. SQLite
// answers this way; the contract run checks PostgreSQL and MySQL against it.
func TestIdenticalValueUpdateCountsAsOneRow(t *testing.T) {
	store, file := newChangeStore(t, itemsSeed,
		`INSERT INTO items (id, name, note) VALUES (1, 'alpha', 'n1')`)

	applied, err := store.ApplyChanges(itemSet(file,
		RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
			Edits: []CellEdit{{Column: "name", Value: "alpha"}}},
	))
	if err != nil {
		t.Fatalf("an unchanged value must not read as a lost row: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want the matched row counted", applied)
	}
}

// TestApplyChangesRefusesBeforeTouchingTheServer pins the other half of "none
// does": every rejection the builder can raise has to happen before a
// transaction is opened, so a bad queue leaves the file exactly as it was.
func TestApplyChangesRefusesBeforeTouchingTheServer(t *testing.T) {
	cases := []struct {
		name  string
		set   func(file string) ChangeSet
		inErr string
	}{
		{"key column missing from the result", func(file string) ChangeSet {
			set := itemSet(file, RowChange{
				Original: []interface{}{"alpha", "n1"},
				Edits:    []CellEdit{{Column: "name", Value: "x"}}})
			set.Columns = []string{"name", "note"}
			return set
		}, "does not include key column id"},
		{"empty queue", func(file string) ChangeSet { return itemSet(file) }, "nothing to save"},
		{"an update with nothing edited in it", func(file string) ChangeSet {
			return itemSet(file, RowChange{Original: []interface{}{int64(1), "alpha", "n1"}})
		}, "nothing to save"},
		{"unknown change kind", func(file string) ChangeSet {
			return itemSet(file, RowChange{Kind: "upsert", Original: []interface{}{int64(1), "alpha", "n1"},
				Edits: []CellEdit{{Column: "name", Value: "x"}}})
		}, "unknown row change kind"},
		{"value that is not the column's type", func(file string) ChangeSet {
			return itemSet(file, RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
				Edits: []CellEdit{{Column: "id", Value: "twelve"}}})
		}, "is not a valid"},
		{"table the catalog does not have", func(file string) ChangeSet {
			set := itemSet(file, RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
				Edits: []CellEdit{{Column: "name", Value: "x"}}})
			set.Table = "missing_table"
			return set
		}, "not a plain column"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, file := newChangeStore(t, itemsSeed,
				`INSERT INTO items (id, name, note) VALUES (1, 'alpha', 'n1')`)
			applied, err := store.ApplyChanges(c.set(file))
			if err == nil {
				t.Fatalf("ApplyChanges accepted a %s queue", c.name)
			}
			if applied != 0 {
				t.Errorf("applied = %d with err %v, want 0", applied, err)
			}
			if !strings.Contains(err.Error(), c.inErr) {
				t.Errorf("err = %v, want it to mention %q", err, c.inErr)
			}
			if got := dumpTable(t, store, file, "items"); got != "1|alpha|n1" {
				t.Errorf("table = %q, a refused batch must leave it untouched", got)
			}
		})
	}
}

// TestCompositeKeyLocatesOneRowOnTheServer is the write-side twin of the paging
// key tests: a two-column key has to carry both columns into the WHERE, or the
// batch rewrites a row the user never selected. NULL in a key part has to be
// compared with IS NULL, which this table can actually hold.
func TestCompositeKeyLocatesOneRowOnTheServer(t *testing.T) {
	store, file := newChangeStore(t,
		`CREATE TABLE pair (a TEXT, b TEXT, v TEXT, PRIMARY KEY (a, b))`,
		`INSERT INTO pair (a, b, v) VALUES ('x', NULL, 'one'), ('y', NULL, 'two'), ('x', 'z', 'three')`)

	set := ChangeSet{
		ConnID: changeConnID, Database: file, Table: "pair",
		Columns: []string{"a", "b", "v"},
		Changes: []RowChange{{
			Original: []interface{}{"x", nil, "one"},
			Edits:    []CellEdit{{Column: "v", Value: "updated"}},
		}},
	}
	statements, err := store.BuildChanges(set)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(statements) != 1 || !strings.Contains(statements[0], `"b" IS NULL`) ||
		!strings.Contains(statements[0], `"a" = 'x'`) {
		t.Fatalf("statement = %q, want both key columns in the WHERE", statements[0])
	}
	applied, err := store.ApplyChanges(set)
	if err != nil || applied != 1 {
		t.Fatalf("apply: applied %d, err %v", applied, err)
	}
	want := "x|NULL|updated\nx|z|three\ny|NULL|two"
	if got := dumpTable(t, store, file, "pair"); got != want {
		t.Errorf("table =\n%s\nwant only the one keyed row changed", got)
	}
}

// TestApplyChangesLeavesNoTransactionOpen is the leak check: a rolled back batch
// must give the pool its write lock back. SQLite refuses a second writer, so a
// transaction left open by the rollback turns the next save into an error.
func TestApplyChangesLeavesNoTransactionOpen(t *testing.T) {
	store, file := newChangeStore(t, itemsSeed,
		`INSERT INTO items (id, name, note) VALUES (1, 'alpha', 'n1')`)

	if _, err := store.ApplyChanges(itemSet(file,
		RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
			Edits: []CellEdit{{Column: "name", Value: "renamed"}}},
		RowChange{Kind: RowDelete, Original: []interface{}{int64(99), "gone", "n9"}},
	)); err == nil {
		t.Fatal("the first batch was expected to abort")
	}
	if _, err := store.ApplyChanges(itemSet(file,
		RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
			Edits: []CellEdit{{Column: "name", Value: "renamed"}}},
	)); err != nil {
		t.Fatalf("a save after a rolled back batch must still work: %v", err)
	}
	if got := dumpTable(t, store, file, "items"); got != "1|renamed|n1" {
		t.Errorf("table = %q, want the retry committed", got)
	}
}

// TestApplyChangesRejectsAnUnavailableConnection keeps the entry point honest
// about the session: a queue aimed at a connection that is not live is refused.
// The same queue after reconnecting lands, so the refusal is about the session
// and not about the harness.
func TestApplyChangesRejectsAnUnavailableConnection(t *testing.T) {
	store, file := newChangeStore(t, itemsSeed,
		`INSERT INTO items (id, name, note) VALUES (1, 'alpha', 'n1')`)

	set := itemSet(file, RowChange{Original: []interface{}{int64(1), "alpha", "n1"},
		Edits: []CellEdit{{Column: "name", Value: "x"}}})
	store.Disconnect(changeConnID)
	applied, err := store.ApplyChanges(set)
	if err == nil || applied != 0 {
		t.Fatalf("applying to a closed connection: applied %d, err %v", applied, err)
	}

	if err := store.Connect(ConnConfig{
		ID: changeConnID, Name: "changes", Type: DbTypeSQLite, Database: file,
	}); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if applied, err = store.ApplyChanges(set); err != nil || applied != 1 {
		t.Fatalf("after reconnect: applied %d, err %v", applied, err)
	}
	if got := dumpTable(t, store, file, "items"); got != "1|x|n1" {
		t.Errorf("table = %q, want the retried save committed", got)
	}
}
