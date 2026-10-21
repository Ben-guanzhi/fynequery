package db

import (
	"context"
	"strings"
	"testing"
)

// The CSV import is three layers that can be told apart by what they can touch:
// a parser over bytes, a renderer over already-rendered literals, and one round
// trip to resolve the table. The first two are proved here without a server;
// the round trip and the transaction are proved in csvimport_apply_test.go.

// TestReadCSVRowsTakesTheBOMOffTheFirstName is the Excel case: "CSV UTF-8" is
// saved with a byte order mark, which would otherwise sit inside the first
// column name and match no column of the table.
func TestReadCSVRowsTakesTheBOMOffTheFirstName(t *testing.T) {
	header, rows, err := ReadCSVRows(context.Background(),
		strings.NewReader("\xEF\xBB\xBFid,name\n1,alpha\n"), true)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(header) != 2 || header[0] != "id" || header[1] != "name" {
		t.Fatalf("header = %q, want [id name]", header)
	}
	if len(rows) != 1 || len(rows[0].Cells) != 2 || rows[0].Cells[0] != "1" {
		t.Fatalf("rows = %v, want one row of two cells", rows)
	}
}

// TestReadCSVRowsKeepsQuotedFieldsWhole is what encoding/csv is for: a comma,
// a newline and a doubled quote all belong to the value.
func TestReadCSVRowsKeepsQuotedFieldsWhole(t *testing.T) {
	in := "a,b,c\n\"x,1\",\"line\nbreak\",plain\n\"say \"\"hi\"\"\",z,w\n"
	header, rows, err := ReadCSVRows(context.Background(), strings.NewReader(in), true)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(header) != 3 {
		t.Fatalf("header = %q, want 3 columns", header)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	want := [][]string{{"x,1", "line\nbreak", "plain"}, {"say \"hi\"", "z", "w"}}
	for i, cells := range want {
		if got := strings.Join(rows[i].Cells, "|"); got != strings.Join(cells, "|") {
			t.Errorf("row %d = %q, want %q", i, got, cells)
		}
	}
	// The quoted field spans two lines, so the next record starts on line 4 -
	// which is the whole reason a row carries a line instead of an index.
	if rows[0].Line != 2 || rows[1].Line != 4 {
		t.Errorf("lines = %d, %d; want 2, 4", rows[0].Line, rows[1].Line)
	}
}

// TestReadCSVRowsReadsCRLF is Excel's other habit: rows end with CRLF, and the
// carriage return is not part of the last cell.
func TestReadCSVRowsReadsCRLF(t *testing.T) {
	_, rows, err := ReadCSVRows(context.Background(), strings.NewReader("id\r\n1\r\n2\r\n"), true)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 || rows[0].Cells[0] != "1" || rows[1].Cells[0] != "2" {
		t.Fatalf("rows = %v, want 1 and 2 with no stray carriage return", rows)
	}
}

// TestReadCSVRowsRejectsARaggedRow pins the width rule: a row short of a field
// is a broken file far more often than a row that meant to leave its tail NULL,
// and the error names the line so the file can be fixed.
func TestReadCSVRowsRejectsARaggedRow(t *testing.T) {
	_, _, err := ReadCSVRows(context.Background(), strings.NewReader("id,name\n1,alpha\n2\n"), true)
	if err == nil {
		t.Fatal("a ragged row must be refused")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error = %v, want it to name line 3", err)
	}
}

// TestReadCSVRowsRejectsTextThatIsNotUTF8 is the one encoding this importer
// takes. Guessing a code page would put different characters in the table than
// the file holds, so the refusal names the fix.
func TestReadCSVRowsRejectsTextThatIsNotUTF8(t *testing.T) {
	_, _, err := ReadCSVRows(context.Background(),
		strings.NewReader("id,name\n1,\xff\xfe\n"), true)
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("error = %v, want a UTF-8 refusal", err)
	}
}

func TestReadCSVRowsRejectsAnEmptyFile(t *testing.T) {
	if _, _, err := ReadCSVRows(context.Background(), strings.NewReader(""), true); err == nil {
		t.Error("an empty file must be refused")
	}
}

// TestReadCSVRowsRejectsAHeaderWithoutRows would otherwise import zero rows and
// report success, which reads as "the import worked".
func TestReadCSVRowsRejectsAHeaderWithoutRows(t *testing.T) {
	if _, _, err := ReadCSVRows(context.Background(), strings.NewReader("id,name\n"), true); err == nil {
		t.Error("a header row with no data must be refused")
	}
}

// TestReadCSVRowsWithoutAHeader says where the names come from: the first
// record is data, and it is still returned as the header so the caller knows
// how wide the file is.
func TestReadCSVRowsWithoutAHeader(t *testing.T) {
	header, rows, err := ReadCSVRows(context.Background(), strings.NewReader("1,alpha\n2,beta\n"), false)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(header) != 2 {
		t.Errorf("header = %q, want the first record so its width is known", header)
	}
	if len(rows) != 2 || rows[0].Line != 1 || rows[1].Line != 2 {
		t.Errorf("rows = %v, want two rows starting on lines 1 and 2", rows)
	}
}

// TestReadCSVRowsStopsWhenTheContextIsCanceled is what lets closing the dialog
// abandon a large file instead of parsing it to the end first.
func TestReadCSVRowsStopsWhenTheContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := ReadCSVRows(ctx, strings.NewReader("id\n1\n"), true); err == nil {
		t.Error("a canceled context must stop the parse")
	}
}

// TestMatchCSVColumnsMatchesNamesIgnoringCase is the header row's job: the
// file's spelling should not decide whether a column is found.
func TestMatchCSVColumnsMatchesNamesIgnoringCase(t *testing.T) {
	cols := []ColumnInfo{{Name: "ID"}, {Name: "Name"}, {Name: "Note"}}
	names, err := MatchCSVColumns(cols, []string{"id", "NAME"}, true)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if got := strings.Join(names, ","); got != "ID,Name" {
		t.Errorf("names = %q, want the table's own spelling: ID,Name", got)
	}
}

// TestMatchCSVColumnsReportsEveryUnknownNameTogether saves three round trips
// on a file whose header drifted from the table.
func TestMatchCSVColumnsReportsEveryUnknownNameTogether(t *testing.T) {
	cols := []ColumnInfo{{Name: "ID"}, {Name: "Name"}}
	_, err := MatchCSVColumns(cols, []string{"id", "nom", "extra"}, true)
	if err == nil {
		t.Fatal("an unknown column must be refused")
	}
	if !strings.Contains(err.Error(), "nom") || !strings.Contains(err.Error(), "extra") {
		t.Errorf("error = %v, want both unknown names", err)
	}
}

// TestMatchCSVColumnsRejectsADuplicateName would otherwise render the same
// column twice into the INSERT, which no engine accepts.
func TestMatchCSVColumnsRejectsADuplicateName(t *testing.T) {
	cols := []ColumnInfo{{Name: "ID"}, {Name: "Name"}}
	if _, err := MatchCSVColumns(cols, []string{"id", "ID"}, true); err == nil {
		t.Error("a header naming one column twice must be refused")
	}
}

// TestMatchCSVColumnsFallsBackToPosition is a file with no header: its columns
// are the table's first N, in the order a "select all" export writes them in.
func TestMatchCSVColumnsFallsBackToPosition(t *testing.T) {
	cols := []ColumnInfo{{Name: "ID"}, {Name: "Name"}, {Name: "Note"}}
	names, err := MatchCSVColumns(cols, []string{"1", "alpha"}, false)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if got := strings.Join(names, ","); got != "ID,Name" {
		t.Errorf("names = %q, want the table's first two columns", got)
	}
}

// TestMatchCSVColumnsRejectsAWiderFileThanTheTable would otherwise have to
// invent somewhere to put the surplus columns.
func TestMatchCSVColumnsRejectsAWiderFileThanTheTable(t *testing.T) {
	cols := []ColumnInfo{{Name: "ID"}}
	if _, err := MatchCSVColumns(cols, []string{"1", "2"}, false); err == nil {
		t.Error("more file columns than table columns must be refused")
	}
}

// TestBuildImportBatchesRendersEachEngine'sOwnInsert is the text the preview
// shows and the transaction runs. Oracle is the odd one out on purpose: a plain
// VALUES clause there holds one row, so it gets the multi-table form.
func TestBuildImportBatchesRendersEachEnginesOwnInsert(t *testing.T) {
	names := []string{"id", "name"}
	types := map[string]string{"id": "int", "name": "text"}
	rows := []CSVRow{{Line: 2, Cells: []string{"1", "a"}}, {Line: 3, Cells: []string{"2", "b"}}}

	cases := []struct {
		dbType string
		want   string
	}{
		{DbTypeMySQL, "INSERT INTO items (`id`, `name`) VALUES (1, 'a'), (2, 'b')"},
		{DbTypePostgres, `INSERT INTO items ("id", "name") VALUES (1, 'a'), (2, 'b')`},
		{DbTypeSQLite, `INSERT INTO items ("id", "name") VALUES (1, 'a'), (2, 'b')`},
		// SQL Server's literals carry the N prefix because the shared
		// literal does - the same one a grid edit's UPDATE renders with -
		// which is what keeps a non-ASCII value from arriving as a
		// single-byte string on that engine only.
		{DbTypeSQLServer, "INSERT INTO items ([id], [name]) VALUES (1, N'a'), (2, N'b')"},
		{DbTypeOracle, `INSERT ALL INTO items ("id", "name") VALUES (1, 'a') INTO items ("id", "name") VALUES (2, 'b') SELECT 1 FROM dual`},
	}
	for _, tc := range cases {
		batches, err := BuildImportBatches(tc.dbType, "items", names, types, rows)
		if err != nil {
			t.Fatalf("%s: build: %v", tc.dbType, err)
		}
		if len(batches) != 1 {
			t.Fatalf("%s: got %d batches, want 1", tc.dbType, len(batches))
		}
		if batches[0].Statement != tc.want {
			t.Errorf("%s statement =\n%s\nwant\n%s", tc.dbType, batches[0].Statement, tc.want)
		}
	}
}

// TestBuildImportBatchesChunksByWhatTheEngineCarries is why the batch size
// lives on the dialect: SQLite compiles a multi-row VALUES as a compound
// select and refuses more than 500 terms, so 1200 rows have to go as three
// statements. The batch also has to know the lines it came from.
func TestBuildImportBatchesChunksByWhatTheEngineCarries(t *testing.T) {
	rows := make([]CSVRow, 1200)
	for i := range rows {
		rows[i] = CSVRow{Line: i + 2, Cells: []string{"x"}}
	}
	batches, err := BuildImportBatches(DbTypeSQLite, "items", []string{"name"},
		map[string]string{"name": "text"}, rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(batches) != 3 {
		t.Fatalf("got %d batches, want 3 (500 + 500 + 200)", len(batches))
	}
	if batches[0].Rows != 500 || batches[1].Rows != 500 || batches[2].Rows != 200 {
		t.Errorf("batch sizes = %d, %d, %d; want 500, 500, 200",
			batches[0].Rows, batches[1].Rows, batches[2].Rows)
	}
	if batches[0].FirstLine != 2 || batches[1].FirstLine != 502 || batches[2].FirstLine != 1002 {
		t.Errorf("first lines = %d, %d, %d; want 2, 502, 1002",
			batches[0].FirstLine, batches[1].FirstLine, batches[2].FirstLine)
	}
	if n := strings.Count(batches[0].Statement, "('x')"); n != 500 {
		t.Errorf("batch 1 carries %d rows, want 500", n)
	}
}

// TestBuildImportBatchesSendsABlankCellAsNULL is the one place the importer
// decides something csv cannot: the format does not distinguish an empty cell
// from an empty string, and NULL is what a column left out of a row means.
func TestBuildImportBatchesSendsABlankCellAsNULL(t *testing.T) {
	rows := []CSVRow{{Line: 2, Cells: []string{"1", "", "  "}}}
	batches, err := BuildImportBatches(DbTypeSQLite, "items",
		[]string{"id", "note", "extra"}, map[string]string{"id": "int"}, rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got, want := batches[0].Statement, `INSERT INTO items ("id", "note", "extra") VALUES (1, NULL, NULL)`; got != want {
		t.Errorf("statement =\n%s\nwant\n%s", got, want)
	}
}

// TestBuildImportBatchesRefusesAValueTheColumnCannotHold is the shared
// valueLiteral doing its job through the importer too: text in a numeric
// column is refused here rather than silently becoming 0 on the server.
func TestBuildImportBatchesRefusesAValueTheColumnCannotHold(t *testing.T) {
	rows := []CSVRow{{Line: 7, Cells: []string{"twelve"}}}
	_, err := BuildImportBatches(DbTypeSQLite, "items", []string{"id"},
		map[string]string{"id": "int"}, rows)
	if err == nil {
		t.Fatal("text in a numeric column must be refused")
	}
	if !strings.Contains(err.Error(), "line 7") || !strings.Contains(err.Error(), "id") {
		t.Errorf("error = %v, want it to name the line and the column", err)
	}
}

// TestBuildImportBatchesRefusesANUL would otherwise truncate the statement
// mid-value on the way to the server.
func TestBuildImportBatchesRefusesANUL(t *testing.T) {
	rows := []CSVRow{{Line: 3, Cells: []string{"a\x00b"}}}
	if _, err := BuildImportBatches(DbTypeSQLite, "items", []string{"name"},
		map[string]string{"name": "text"}, rows); err == nil {
		t.Error("a NUL in a value must be refused")
	}
}

// TestBuildImportBatchesRefusesARowOfAnotherWidth guards the second width
// check: ReadCSVRows only knows a file is internally consistent, not that it
// matches the columns the header resolved to.
func TestBuildImportBatchesRefusesARowOfAnotherWidth(t *testing.T) {
	rows := []CSVRow{{Line: 4, Cells: []string{"1"}}}
	_, err := BuildImportBatches(DbTypeSQLite, "items", []string{"id", "name"},
		map[string]string{"id": "int"}, rows)
	if err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("error = %v, want it to name line 4", err)
	}
}

// TestBuildImportBatchesRefusesAnUnknownEngine is the negative control for the
// dispatch: an engine the catalogue does not know has no insert form, so it
// must not fall through to a default that happens to look like MySQL's.
func TestBuildImportBatchesRefusesAnUnknownEngine(t *testing.T) {
	if _, err := BuildImportBatches("nosuchengine", "items", []string{"id"}, nil,
		[]CSVRow{{Line: 1, Cells: []string{"1"}}}); err == nil {
		t.Error("an unknown engine must be refused")
	}
}

// TestBuildImportBatchesRefusesNoColumns keeps the empty case from rendering
// "INSERT INTO t () VALUES ()", which is noise in a preview and an error on
// every engine.
func TestBuildImportBatchesRefusesNoColumns(t *testing.T) {
	if _, err := BuildImportBatches(DbTypeSQLite, "items", nil, nil,
		[]CSVRow{{Line: 1, Cells: []string{"1"}}}); err == nil {
		t.Error("no columns must be refused")
	}
}

// TestBuildImportBatchesQuotesAReservedName is what quoteIdent is for: a
// column called "order" is only an identifier when the engine quotes it.
func TestBuildImportBatchesQuotesAReservedName(t *testing.T) {
	rows := []CSVRow{{Line: 2, Cells: []string{"1"}}}
	batches, err := BuildImportBatches(DbTypeMySQL, "items", []string{"order"}, nil, rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(batches[0].Statement, "`order`") {
		t.Errorf("statement = %s, want the column quoted", batches[0].Statement)
	}
}
