package db

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// CSV import: the other half of the export the grid already has.
//
// Everything here is either a pure function of bytes (ReadCSVRows,
// MatchCSVColumns, BuildImportBatches) or one round trip to resolve the table
// (BuildCSVImport), so the UI can build the whole batch list in the background
// before it shows the user anything - and what it shows is exactly what it then
// runs. The literal rendering goes through valueLiteral, the same one the grid's
// edits use, so a value that reaches the server from a file is escaped the way
// a value typed into a cell is.

// csvImportMaxRows bounds one import. The statements are built in memory as
// text, so an unbounded file is an unbounded allocation; past this the file has
// to be split first, which is also the honest answer for a server that would
// have held the whole transaction open for it.
const csvImportMaxRows = 500000

// CSVRow is one parsed record. Line is its 1-based first line in the file, the
// one quoted fields with embedded newlines push later records out of sync with
// a plain record count - so an error names the line the user can look at.
type CSVRow struct {
	Line  int
	Cells []string
}

// CSVImportRequest describes one import: where the rows go and which file they
// come from.
type CSVImportRequest struct {
	ConnID    string
	Database  string
	Schema    string
	Table     string
	HasHeader bool
	Path      string
}

// ImportBatch is one statement worth of rows, plus the file lines it came from
// so a failure can point at them.
type ImportBatch struct {
	Statement string
	FirstLine int
	Rows      int
}

// stripBOM drops a UTF-8 byte order mark. Excel writes one on every "CSV UTF-8"
// save, and it otherwise ends up inside the first column name, which then
// matches no column of the table.
func stripBOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	mark, err := br.Peek(3)
	if err != nil && err != io.EOF {
		return br
	}
	if len(mark) >= 3 && mark[0] == 0xEF && mark[1] == 0xBB && mark[2] == 0xBF {
		if _, err := br.Discard(3); err != nil {
			return br
		}
	}
	return br
}

// ReadCSVRows parses a CSV file into rows. hasHeader says the first record
// names columns rather than holding data; header is that first record either
// way, so the caller always knows the file's width.
//
// The encoding rules are encoding/csv's: CRLF, quoted fields, embedded commas
// and newlines, and a doubled quote for a literal one. LazyQuotes stays off on
// purpose - a stray quote in a file is a signal that the file is not the shape
// the importer thinks it is, and guessing at it silently relocates every field
// after it. Every record must also be as wide as the first one: a short row is
// more often a broken file than a row that meant to leave its tail NULL.
//
// utf8 is the only accepted encoding. A GBK file from an older Excel would need
// a second decoder and a guess about the code page, and the guess is the part
// that quietly corrupts data; refusing it names the fix (save as UTF-8).
func ReadCSVRows(ctx context.Context, r io.Reader, hasHeader bool) (header []string, rows []CSVRow, err error) {
	cr := csv.NewReader(stripBOM(r))
	// The first record fixes the width; every later one is checked against it.
	cr.FieldsPerRecord = 0
	cr.TrimLeadingSpace = false

	first, readErr := cr.Read()
	if readErr == io.EOF {
		return nil, nil, fmt.Errorf("the file has no rows")
	}
	if readErr != nil {
		return nil, nil, csvError(readErr)
	}
	if len(first) == 0 {
		return nil, nil, fmt.Errorf("the file has no columns")
	}
	header = first
	if !hasHeader {
		firstRow := makeRow(cr, first)
		if err := checkRowEncoding(firstRow); err != nil {
			return nil, nil, err
		}
		rows = append(rows, firstRow)
	}

	seen := 0
	for {
		if seen%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		seen++
		record, readErr := cr.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, csvError(readErr)
		}
		row := makeRow(cr, record)
		if err := checkRowEncoding(row); err != nil {
			return nil, nil, err
		}
		rows = append(rows, row)
		if len(rows) > csvImportMaxRows {
			return nil, nil, fmt.Errorf("the file has more than %d rows; split it before importing", csvImportMaxRows)
		}
	}
	if len(rows) == 0 {
		if hasHeader {
			return header, nil, fmt.Errorf("the file has a header row but no data rows")
		}
		return nil, nil, fmt.Errorf("the file has no rows")
	}
	return header, rows, nil
}

// makeRow pairs a parsed record with the line it started on. csv's FieldPos
// counts the newlines inside quoted fields, which a plain record count does
// not.
func makeRow(r *csv.Reader, record []string) CSVRow {
	row := CSVRow{Cells: record}
	if len(record) > 0 {
		if line, _ := r.FieldPos(0); line > 0 {
			row.Line = line
		}
	}
	return row
}

// checkRowEncoding rejects a row that is not UTF-8, naming its line so the
// user can find it instead of hunting a replacement character in a grid.
func checkRowEncoding(row CSVRow) error {
	for _, cell := range row.Cells {
		if !utf8.ValidString(cell) {
			return fmt.Errorf("line %d is not UTF-8 text; save the file as UTF-8 and import it again", row.Line)
		}
	}
	return nil
}

// csvError rewrites csv's parse error into a message that names the file's
// line, which for a quoted field spanning several lines is not the record's
// number.
func csvError(err error) error {
	if pe, ok := err.(*csv.ParseError); ok && pe.StartLine > 0 {
		return fmt.Errorf("line %d: %v", pe.StartLine, pe.Err)
	}
	return err
}

// MatchCSVColumns resolves a file's columns against the table's.
//
// With a header row, names are matched case-insensitively and every name the
// table does not have is reported together - one dialog listing three unknown
// columns beats three runs that each fix one. A file without a header carries
// no names, so its columns are taken to be the table's first N in catalog
// order, which is the order a "select all" export writes them in.
func MatchCSVColumns(cols []ColumnInfo, header []string, hasHeader bool) ([]string, error) {
	if len(cols) == 0 {
		return nil, fmt.Errorf("the table has no columns")
	}
	if !hasHeader {
		if len(header) > len(cols) {
			return nil, fmt.Errorf("the file has %d columns but the table has %d", len(header), len(cols))
		}
		width := len(header)
		if width == 0 {
			return nil, fmt.Errorf("the file has no columns")
		}
		names := make([]string, width)
		for i := range names {
			names[i] = cols[i].Name
		}
		return names, nil
	}

	names := make([]string, 0, len(header))
	var unknown []string
	seen := make(map[string]bool, len(header))
	for _, h := range header {
		name := strings.TrimSpace(h)
		lower := strings.ToLower(name)
		if lower == "" {
			unknown = append(unknown, fmt.Sprintf("column %d (empty)", len(names)+1))
			continue
		}
		if seen[lower] {
			return nil, fmt.Errorf("the file names column %q twice", name)
		}
		seen[lower] = true
		found := ""
		for _, col := range cols {
			if strings.ToLower(col.Name) == lower {
				found = col.Name
				break
			}
		}
		if found == "" {
			unknown = append(unknown, name)
			continue
		}
		names = append(names, found)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("the table has no column named %s", strings.Join(unknown, ", "))
	}
	return names, nil
}

// BuildImportBatches renders the rows as INSERT statements, batched by what the
// engine can carry in one statement. table is already qualified and every value
// is rendered by valueLiteral, so the only per-engine decision left is how the
// rows are punctuated - which is the dialect's insertRows.
func BuildImportBatches(dbType, table string, names []string, types map[string]string, rows []CSVRow) ([]ImportBatch, error) {
	d, err := dialectFor(dbType)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no columns to import into")
	}
	quoted := make([]string, len(names))
	for i, name := range names {
		ident, err := quoteIdent(dbType, name)
		if err != nil {
			return nil, err
		}
		quoted[i] = ident
	}

	batches := make([]ImportBatch, 0, len(rows)/d.maxInsertRows+1)
	var literalRows [][]string
	for _, row := range rows {
		if len(row.Cells) != len(names) {
			return nil, fmt.Errorf("line %d has %d fields, expected %d", row.Line, len(row.Cells), len(names))
		}
		literals := make([]string, len(row.Cells))
		for i, cell := range row.Cells {
			text := strings.TrimSpace(cell)
			if text == "" {
				// A blank cell means "no value for this column". csv cannot
				// tell `,` from `""`, and NULL is what someone exporting a
				// column with gaps meant by both.
				literals[i] = "NULL"
				continue
			}
			lit, err := valueLiteral(dbType, types[strings.ToLower(names[i])], text)
			if err != nil {
				return nil, fmt.Errorf("line %d, column %s: %w", row.Line, names[i], err)
			}
			literals[i] = lit
		}
		literalRows = append(literalRows, literals)
	}

	// Chunk after rendering so a batch is never cut mid-row by the limit.
	for start := 0; start < len(literalRows); start += d.maxInsertRows {
		end := start + d.maxInsertRows
		if end > len(literalRows) {
			end = len(literalRows)
		}
		chunk := literalRows[start:end]
		batches = append(batches, ImportBatch{
			Statement: d.insertRows(table, quoted, chunk),
			FirstLine: rows[start].Line,
			Rows:      len(chunk),
		})
	}
	if len(batches) == 0 {
		return nil, fmt.Errorf("no rows to import")
	}
	return batches, nil
}

// BuildCSVImport resolves the table's columns and renders the file's rows into
// the statements that would import them. It touches the server once, for the
// column list, and reads the file once - so the preview the UI shows and the
// batch it later executes are the same slice, built from the same catalog read.
func (c *Store) BuildCSVImport(ctx context.Context, req CSVImportRequest) ([]ImportBatch, error) {
	conn, err := c.OpenConn(req.ConnID)
	if err != nil {
		return nil, err
	}
	if req.Table == "" {
		return nil, fmt.Errorf("no table to import into")
	}
	schema := DefaultSchema(conn.Config.Type, req.Schema)
	name, err := TableName(conn.Config.Type, req.Database, schema, req.Table)
	if err != nil {
		return nil, err
	}
	cols, err := c.Columns(ctx, req.ConnID, req.Database, schema, req.Table)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(req.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	header, rows, err := ReadCSVRows(ctx, f, req.HasHeader)
	if err != nil {
		return nil, err
	}
	names, err := MatchCSVColumns(cols, header, req.HasHeader)
	if err != nil {
		return nil, err
	}
	types := make(map[string]string, len(cols))
	for _, col := range cols {
		types[strings.ToLower(col.Name)] = col.Type
	}
	return BuildImportBatches(conn.Config.Type, name, names, types, rows)
}

// ExecImportBatches runs batches built by BuildCSVImport as one transaction:
// either the file lands or none of it does. A failing batch names the file
// lines it came from rather than its index, because the index is a detail of
// how the importer chunks and the lines are what the user can open.
func (c *Store) ExecImportBatches(ctx context.Context, req CSVImportRequest, batches []ImportBatch) (int64, error) {
	if len(batches) == 0 {
		return 0, fmt.Errorf("nothing to import")
	}
	statements := make([]string, len(batches))
	for i, b := range batches {
		statements[i] = b.Statement
	}
	return c.applyInTx(ctx, req.ConnID, req.Database, statements, func(i int) string {
		b := batches[i]
		if b.Rows <= 1 {
			return fmt.Sprintf("batch %d (line %d)", i+1, b.FirstLine)
		}
		return fmt.Sprintf("batch %d (lines %d-%d)", i+1, b.FirstLine, b.FirstLine+b.Rows-1)
	})
}
