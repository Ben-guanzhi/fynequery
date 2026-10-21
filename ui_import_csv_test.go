package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// The import dialog's own logic, minus the file chooser: what the preview
// prints, what the header counts, and the two refusals that happen before any
// of that. The round trip behind the preview is csvimport_apply_test.go's, on
// a real SQLite transaction.

// TestImportPreviewPrintsTheFirstStatementsAndCountsTheRest is the promise the
// dialog makes: what is on screen is a slice of the batches it will run, so
// hiding the tail is a statement about the display, not about the import.
func TestImportPreviewPrintsTheFirstStatementsAndCountsTheRest(t *testing.T) {
	batches := make([]db.ImportBatch, 23)
	for i := range batches {
		batches[i] = db.ImportBatch{
			Statement: fmt.Sprintf("INSERT INTO items VALUES (%d)", i),
			FirstLine: i + 2, Rows: 1,
		}
	}
	text := importPreviewText(batches)

	if !strings.Contains(text, "INSERT INTO items VALUES (0)") {
		t.Error("the preview must start with the first batch")
	}
	if strings.Contains(text, "INSERT INTO items VALUES (20);") {
		t.Error("statements past the preview limit must not be printed")
	}
	if !strings.Contains(text, "其余 3 条语句已省略") {
		t.Errorf("preview = %q, want it to say 3 batches are hidden", text)
	}
	if !strings.Contains(text, "执行时会全部执行") {
		t.Errorf("preview = %q, want it to say the hidden ones still run", text)
	}
}

// TestImportPreviewCountsRowsNotStatements is the number the header shows: a
// file of 1000 rows is two statements on SQLite, and "分 2 条语句" is an
// artefact of the engine's batch size, not what the user is importing.
func TestImportPreviewCountsRowsNotStatements(t *testing.T) {
	batches := []db.ImportBatch{{Rows: 500}, {Rows: 500}}
	if got := importRowCount(batches); got != 1000 {
		t.Errorf("importRowCount = %d, want 1000", got)
	}
}

// TestImportTargetLabel names the table the way the engine addresses it, so
// the dialog says which database of the connection is about to change.
func TestImportTargetLabel(t *testing.T) {
	cases := []struct {
		nd   nodeData
		want string
	}{
		{nodeData{db: "shop", schema: "", name: "orders"}, "shop.orders"},
		{nodeData{db: "shop", schema: "dbo", name: "orders"}, "shop.dbo.orders"},
		{nodeData{db: "", schema: "public", name: "orders"}, "public.orders"},
	}
	for _, tc := range cases {
		nd := tc.nd
		if got := importTargetLabel(&nd); got != tc.want {
			t.Errorf("importTargetLabel(%+v) = %q, want %q", nd, got, tc.want)
		}
	}
}

// TestLocalFilePathKeepsTheWindowsDrive is the path os.Open is handed. A
// Windows file URI carries a slash before the drive letter, which os.Open
// would read as a relative path and fail to find.
func TestLocalFilePathKeepsTheWindowsDrive(t *testing.T) {
	if got := localFilePath(storage.NewFileURI("/C:/data/rows.csv")); got != "C:/data/rows.csv" {
		t.Errorf("localFilePath = %q, want the drive letter kept", got)
	}
	if got := localFilePath(storage.NewFileURI("/home/u/rows.csv")); got != "/home/u/rows.csv" {
		t.Errorf("localFilePath = %q, want a unix path untouched", got)
	}
}

// TestImportRefusesAView would otherwise run a file's rows into an object that
// has none, and come back with an error the user cannot act on.
func TestImportRefusesAView(t *testing.T) {
	c := newImportTestClient(t)
	c.importCSVDialog(&nodeData{db: "shop", name: "v_orders", typ: "view"})
	if !strings.Contains(c.statusText(), "不能导入") {
		t.Errorf("status = %q, want the view to be refused", c.statusText())
	}
}

// TestImportRefusesWithoutAConnection is the same refusal one step earlier: no
// session means nowhere to send the rows, and saying so beats a file chooser
// that leads nowhere.
func TestImportRefusesWithoutAConnection(t *testing.T) {
	c := newImportTestClient(t)
	c.importCSVDialog(&nodeData{db: "shop", name: "orders", typ: "table"})
	if !strings.Contains(c.statusText(), "请先连接") {
		t.Errorf("status = %q, want the missing connection to be reported", c.statusText())
	}
}

// newImportTestClient builds a client with no connection open, which is the
// state both refusals above are about. The status strip is built by hand
// because these tests never assemble the grid below it.
func newImportTestClient(t *testing.T) *Client {
	t.Helper()
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	// The real UI sets picker.idx = -1 when it builds the connections panel;
	// this client never builds it, so pin -1 here. Otherwise idx defaults to 0
	// and selectedConn() reports saved[0] whenever the shared state dir has any
	// connection - which would send the "no connection" test down the file
	// dialog instead of the refusal.
	c.picker.idx = -1
	w.SetContent(c.buildQueryView())
	c.status = widget.NewLabel("就绪")
	return c
}

// TestImportBuildsTheWholeFileIntoBatches is the preview's premise, checked
// against a real catalog and a real file: the build reads the columns once and
// returns every statement, and the row count it reports is the file's rows.
// It is the UI's call into the same path csvimport_apply_test.go commits.
func TestImportBuildsTheWholeFileIntoBatches(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rows.db")
	c := newImportTestClient(t)
	if err := c.store.Connect(db.ConnConfig{
		ID: "imp", Name: "import", Type: db.DbTypeSQLite, Database: file,
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(c.store.CloseAll)
	conn, err := c.store.OpenConn("imp")
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	if res := c.store.Execute(context.Background(), conn, file,
		`CREATE TABLE items (id INTEGER, name TEXT)`, 0); res.Error != "" {
		t.Fatalf("seed: %s", res.Error)
	}

	path := filepath.Join(dir, "rows.csv")
	var sb strings.Builder
	sb.WriteString("id,name\n")
	for i := 1; i <= 1200; i++ {
		fmt.Fprintf(&sb, "%d,name%d\n", i, i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0600); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	req := db.CSVImportRequest{
		ConnID: "imp", Database: file, Table: "items", HasHeader: true, Path: path,
	}
	var (
		batches  []db.ImportBatch
		buildErr error
	)
	c.background(func(ctx context.Context) {
		batches, buildErr = c.store.BuildCSVImport(ctx, req)
	}, func() {})
	if buildErr != nil {
		t.Fatalf("build: %v", buildErr)
	}
	if len(batches) != 3 {
		t.Fatalf("got %d batches, want 3 (500 + 500 + 200)", len(batches))
	}
	if got := importRowCount(batches); got != 1200 {
		t.Errorf("importRowCount = %d, want 1200", got)
	}
}
