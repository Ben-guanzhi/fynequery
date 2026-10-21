package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The part of the import a pure function cannot prove is that it is one
// transaction: a file of 1000 rows goes in as several statements, and a failure
// in the second one has to leave the table exactly as it was. SQLite is a real
// transaction that needs no server, so this runs everywhere - the same reason
// changes_apply_test.go uses it.

// csvSeed is the table the file lands in: a UNIQUE column is what makes a
// second batch fail on purpose, and a TEXT column is what a numeric column
// refuses to accept.
const csvSeed = `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, note TEXT UNIQUE)`

// writeCSV drops a file of rows under the test's temp dir and names it back.
// withHeader writes a leading "id,name,note" record.
func writeCSV(t *testing.T, withHeader bool, rows []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "import.csv")
	var sb strings.Builder
	if withHeader {
		sb.WriteString("id,name,note\n")
	}
	for _, row := range rows {
		sb.WriteString(row)
		sb.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	return path
}

// csvRequest points an import at the items table.
func csvRequest(file, path string) CSVImportRequest {
	return CSVImportRequest{
		ConnID: changeConnID, Database: file, Table: "items", HasHeader: true, Path: path,
	}
}

// csvRows builds n data rows, optionally with one note repeated so a later
// batch collides with the first.
func csvRows(n, collideAt int) []string {
	rows := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		note := fmt.Sprintf("n%d", i)
		if i == collideAt {
			note = "n1"
		}
		rows = append(rows, fmt.Sprintf("%d,name%d,%s", i, i, note))
	}
	return rows
}

// TestCSVImportCommitsTheWholeFile is the success path: 1000 rows arrive as two
// batches because SQLite's dialect carries 500, and every one of them is in the
// table when it returns.
func TestCSVImportCommitsTheWholeFile(t *testing.T) {
	store, file := newChangeStore(t, csvSeed)
	req := csvRequest(file, writeCSV(t, true, csvRows(1000, 0)))

	batches, err := store.BuildCSVImport(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2 for 1000 rows at 500 each", len(batches))
	}
	applied, err := store.ExecImportBatches(context.Background(), req, batches)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if applied != 1000 {
		t.Errorf("applied = %d, want 1000", applied)
	}
	conn, err := store.OpenConn(changeConnID)
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	res := store.Execute(context.Background(), conn, file, "SELECT COUNT(*) FROM items", 0)
	if res.Error != "" {
		t.Fatalf("count: %s", res.Error)
	}
	if got := fmt.Sprint(res.Rows[0][0]); got != "1000" {
		t.Errorf("rows in the table = %s, want 1000", got)
	}
}

// TestCSVImportRollsBackTheWholeFile is the promise: the first batch's 500 rows
// are already sent when the second one violates the UNIQUE column, and the
// table must still be empty afterwards. A freshly built batch list is also what
// proves the error names the file's lines rather than a statement index.
func TestCSVImportRollsBackTheWholeFile(t *testing.T) {
	store, file := newChangeStore(t, csvSeed)
	req := csvRequest(file, writeCSV(t, true, csvRows(1000, 600)))

	batches, err := store.BuildCSVImport(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, err := store.ExecImportBatches(context.Background(), req, batches); err == nil {
		t.Fatal("a duplicate in the second batch must fail")
	} else if !strings.Contains(err.Error(), "batch 2 (lines 502-1001)") {
		t.Errorf("error = %v, want it to name batch 2 and its lines", err)
	}
	if got := dumpTable(t, store, file, "items"); got != "" {
		t.Errorf("table after a rolled-back import =\n%s\nwant it empty", got)
	}
}

// TestCSVImportStopsOnACanceledContext is what the dialog's 取消 button is for:
// a file that would take a while must not keep writing after the user let go.
func TestCSVImportStopsOnACanceledContext(t *testing.T) {
	store, file := newChangeStore(t, csvSeed)
	req := csvRequest(file, writeCSV(t, true, csvRows(1000, 0)))

	batches, err := store.BuildCSVImport(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ExecImportBatches(ctx, req, batches); err == nil {
		t.Error("a canceled context must stop the import")
	}
	if got := dumpTable(t, store, file, "items"); got != "" {
		t.Errorf("table after a canceled import =\n%s\nwant it empty", got)
	}
}

// TestCSVImportRefusesAFileTheTableCannotTake pins where a bad file is caught:
// in the build, before any statement is rendered, so nothing reaches the
// server. The table is empty because no transaction ever opened.
func TestCSVImportRefusesAFileTheTableCannotTake(t *testing.T) {
	store, file := newChangeStore(t, csvSeed)
	path := writeCSV(t, true, []string{"twelve,name1,n1", "2,name2,n2"})
	req := csvRequest(file, path)

	if _, err := store.BuildCSVImport(context.Background(), req); err == nil {
		t.Fatal("text in the key column must be refused before anything is written")
	}
	if got := dumpTable(t, store, file, "items"); got != "" {
		t.Errorf("table = \n%s, want it empty", got)
	}
}

// TestCSVImportRefusesAColumnTheTableDoesNotHave is the header drift case,
// resolved against the real catalog rather than against a fixture.
func TestCSVImportRefusesAColumnTheTableDoesNotHave(t *testing.T) {
	store, file := newChangeStore(t, csvSeed)
	path := filepath.Join(t.TempDir(), "drift.csv")
	if err := os.WriteFile(path, []byte("id,name,extra\n1,alpha,x\n"), 0600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if _, err := store.BuildCSVImport(context.Background(), csvRequest(file, path)); err == nil {
		t.Fatal("a column the table does not have must be refused")
	}
}

// TestCSVImportRefusesWhenNotConnected is the negative control for the handle:
// an import must not silently go to a pool that has been closed.
func TestCSVImportRefusesWhenNotConnected(t *testing.T) {
	store, file := newChangeStore(t, csvSeed)
	req := csvRequest(file, writeCSV(t, true, csvRows(2, 0)))
	store.CloseAll()
	if _, err := store.BuildCSVImport(context.Background(), req); err == nil {
		t.Error("an import on a closed connection must be refused")
	}
}

// TestCSVImportReadsAFileWithNoHeader is the other shape a file comes in: the
// first record is data, so its columns are the table's first three in catalog
// order - which is the order a "select all" export wrote them in.
func TestCSVImportReadsAFileWithNoHeader(t *testing.T) {
	store, file := newChangeStore(t, csvSeed)
	req := csvRequest(file, writeCSV(t, false, csvRows(3, 0)))
	req.HasHeader = false

	batches, err := store.BuildCSVImport(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	if _, err := store.ExecImportBatches(context.Background(), req, batches); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := dumpTable(t, store, file, "items"); got != "1|name1|n1\n2|name2|n2\n3|name3|n3" {
		t.Errorf("table =\n%s\nwant the three rows", got)
	}
}
