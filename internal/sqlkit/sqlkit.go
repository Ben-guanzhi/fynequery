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
	if maxRows <= 0 {
		maxRows = 1000
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

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
		row := make([]*string, len(colNames))
		for i, v := range vals {
			row[i] = FormatValue(v)
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
