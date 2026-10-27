// Package sqlkit holds the database/sql plumbing shared by the built-in
// MySQL driver and every plugin: running a statement, formatting values into
// grid cells, and identifier quoting. It is imported by both the main app
// (db package) and the plugin executables, so it must not depend on either.
package sqlkit

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fynequery/proto"
)

// Timeout caps any single statement (queries run in background goroutines in
// the UI, so this is a safety net, not a UX timeout).
const Timeout = 10 * time.Minute

// selectPrefixes are statements whose results come back as rows; anything
// else goes through Exec and reports rows affected.
var selectPrefixes = []string{
	"SELECT", "WITH", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "PRAGMA", "TABLE", "VALUES",
}

// IsRowReturning reports whether a statement should be run with Query.
func IsRowReturning(sqlText string) bool {
	first := firstWord(sqlText)
	for _, p := range selectPrefixes {
		if first == p {
			return true
		}
	}
	return false
}

func firstWord(s string) string {
	t := strings.TrimSpace(s)
	if i := strings.IndexAny(t, " \t\r\n;("); i >= 0 {
		t = t[:i]
	}
	return strings.ToUpper(t)
}

// Run executes one statement against db and shapes the outcome for the wire
// protocol: row-returning statements produce columns+rows (capped at
// maxRows), everything else reports rows affected.
func Run(q Querier, sqlText string, maxRows int) (proto.QueryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	return RunCtx(q, ctx, sqlText, maxRows)
}

// RunCtx is Run with the caller's context: a plugin tracks the statements it
// runs per connection so a Cancel request can cancel them mid-flight. The
// caller owns the context's lifetime (and its timeout, if it wants one).
func RunCtx(q Querier, ctx context.Context, sqlText string, maxRows int) (proto.QueryResult, error) {
	if maxRows <= 0 {
		maxRows = 1000
	}
	start := time.Now()

	if !IsRowReturning(sqlText) {
		res, err := q.ExecContext(ctx, sqlText)
		if err != nil {
			return proto.QueryResult{}, err
		}
		n, _ := res.RowsAffected()
		return proto.QueryResult{RowsAffected: n, DurationMS: msSince(start)}, nil
	}

	rows, err := q.QueryContext(ctx, sqlText)
	if err != nil {
		return proto.QueryResult{}, err
	}
	defer rows.Close()
	return ReadRows(rows, maxRows, start)
}

// Querier is the subset of *sql.DB / *sql.Conn used by Run.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// StreamResult reports the outcome of StreamRun.
type StreamResult struct {
	Rows         int64 // rows emitted (row-returning statements)
	RowsAffected int64 // rows affected (non-row-returning statements)
	DurationMS   int64
}

// StreamRun executes a statement and emits its result in batches of up to
// `batch` rows as they are read from the cursor, so a large result set is
// fetched incrementally with bounded memory instead of materialised all at
// once. The column metadata is passed on the first emit only (cols is nil
// afterwards). Non-row-returning statements are Exec'd and emit no rows; their
// RowsAffected is returned in the result. If emit returns an error, streaming
// stops and that error is returned.
func StreamRun(q Querier, sqlText string, batch int, emit func(cols []proto.ColumnInfo, rows [][]proto.CellPayload) error) (StreamResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	return StreamRunCtx(q, ctx, sqlText, batch, emit)
}

// StreamRunCtx is StreamRun with the caller's context (see RunCtx): cancelling
// it aborts both the query and the cursor walk.
func StreamRunCtx(q Querier, ctx context.Context, sqlText string, batch int, emit func(cols []proto.ColumnInfo, rows [][]proto.CellPayload) error) (StreamResult, error) {
	if batch <= 0 {
		batch = 200
	}
	start := time.Now()

	if !IsRowReturning(sqlText) {
		res, err := q.ExecContext(ctx, sqlText)
		if err != nil {
			return StreamResult{}, err
		}
		n, _ := res.RowsAffected()
		return StreamResult{RowsAffected: n, DurationMS: msSince(start)}, nil
	}

	rows, err := q.QueryContext(ctx, sqlText)
	if err != nil {
		return StreamResult{}, err
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return StreamResult{}, err
	}
	ctypes, _ := rows.ColumnTypes()
	cols := make([]proto.ColumnInfo, len(colNames))
	for i := range colNames {
		cols[i].Name = colNames[i]
		if ctypes != nil && i < len(ctypes) {
			cols[i].Type = strings.ToLower(ctypes[i].DatabaseTypeName())
			if nullable, ok := ctypes[i].Nullable(); ok {
				cols[i].Nullable = nullable
			}
		}
	}

	vals := make([]interface{}, len(colNames))
	ptrs := make([]interface{}, len(colNames))
	for i := range colNames {
		ptrs[i] = &vals[i]
	}

	var total int64
	var buf [][]proto.CellPayload
	sentCols := false
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		chunk := make([][]proto.CellPayload, len(buf))
		copy(chunk, buf)
		send := []proto.ColumnInfo(nil)
		if !sentCols {
			send = cols
			sentCols = true
		}
		buf = buf[:0]
		return emit(send, chunk)
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return StreamResult{}, err
		}
		row := make([]proto.CellPayload, len(colNames))
		for i, v := range vals {
			row[i] = FormatCell(v)
		}
		buf = append(buf, row)
		total++
		if len(buf) >= batch {
			if err := flush(); err != nil {
				return StreamResult{Rows: total, DurationMS: msSince(start)}, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return StreamResult{}, err
	}
	if err := flush(); err != nil {
		return StreamResult{Rows: total, DurationMS: msSince(start)}, err
	}
	return StreamResult{Rows: total, DurationMS: msSince(start)}, nil
}

// ReadRows drains rows into a QueryResult. It is exported because metadata
// queries in drivers reuse the same formatting.
func ReadRows(rows *sql.Rows, maxRows int, start time.Time) (proto.QueryResult, error) {
	colNames, err := rows.Columns()
	if err != nil {
		return proto.QueryResult{}, err
	}
	ctypes, _ := rows.ColumnTypes()

	res := proto.QueryResult{Columns: make([]proto.ColumnInfo, len(colNames))}
	vals := make([]interface{}, len(colNames))
	ptrs := make([]interface{}, len(colNames))
	for i := range colNames {
		res.Columns[i].Name = colNames[i]
		if ctypes != nil && i < len(ctypes) {
			res.Columns[i].Type = strings.ToLower(ctypes[i].DatabaseTypeName())
			if nullable, ok := ctypes[i].Nullable(); ok {
				res.Columns[i].Nullable = nullable
			}
		}
		ptrs[i] = &vals[i]
	}

	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return proto.QueryResult{}, err
		}
		row := make([]proto.CellPayload, len(colNames))
		for i, v := range vals {
			row[i] = FormatCell(v)
		}
		res.Rows = append(res.Rows, row)
		if len(res.Rows) >= maxRows {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return proto.QueryResult{}, err
	}
	res.DurationMS = msSince(start)
	return res, nil
}

func msSince(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}

// FormatValue converts a scanned database value into a display cell.
// nil means SQL NULL.
func FormatValue(v interface{}) *string {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		s := string(x)
		return &s
	case string:
		return &x
	case time.Time:
		// Keep fractional seconds (MySQL datetime(6)/timestamp(6)) so an
		// edited cell round-trips without silently truncating precision.
		s := x.Format("2006-01-02 15:04:05.999999999")
		return &s
	case float64:
		s := strconv.FormatFloat(x, 'g', -1, 64)
		return &s
	case float32:
		s := strconv.FormatFloat(float64(x), 'g', -1, 32)
		return &s
	case bool:
		s := strconv.FormatBool(x)
		return &s
	default:
		s := fmt.Sprintf("%v", x)
		return &s
	}
}
