// Package drivercore is the seam between the app and its database drivers.
//
// MySQL and SQLite run in-process through builtinEngine, which wraps a
// *sql.DB the caller opened. Every other engine runs out-of-process as a
// driver plugin - a tiny separate binary that owns its database/sql driver and
// its *sql.DB - and the app talks to it over a length-framed JSON protocol on
// the child's stdin/stdout (see plugin.go / serve.go). Go's native plugin
// package cannot be used here: it is Linux/macOS-only and cgo-bound, so it does
// not work on Windows.
//
// Both backends present the same Engine interface, and both hand back a RowSet
// - the *sql.Rows for the in-process engines, a materialized adapter for the
// out-of-process ones - so the app's query and catalog code never branches on
// which backend answered.
package drivercore

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// ConnConfig is a saved connection definition. It lives here (not in the db
// package) so a driver plugin can build its own DSN from it without importing
// the app; the db package aliases it as db.ConnConfig.
type ConnConfig struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	Host           string            `json:"host"`
	Port           int               `json:"port"`
	Username       string            `json:"username"`
	Password       string            `json:"password"`
	Database       string            `json:"database"`
	SSLMode        string            `json:"sslMode"`
	Options        map[string]string `json:"options,omitempty"`
	DatabaseFilter DatabaseFilter    `json:"databaseFilter"`
}

// DatabaseFilter hides databases in the navigator using SQL-LIKE style masks.
type DatabaseFilter struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

// Allows reports whether a name survives the filter. It lives here so the db
// package (which aliases DatabaseFilter) can call it without redefining a
// method on the aliased type.
func (f DatabaseFilter) Allows(name string) bool {
	if len(f.Include) == 0 && len(f.Exclude) == 0 {
		return true
	}
	for _, p := range f.Exclude {
		if matchMask(p, name) {
			return false
		}
	}
	if len(f.Include) == 0 {
		return true
	}
	for _, p := range f.Include {
		if matchMask(p, name) {
			return true
		}
	}
	return false
}

// matchMask matches a SQL-LIKE style mask against a name.
func matchMask(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	var sb strings.Builder
	sb.WriteString(`(?i)\A`)
	for _, r := range pattern {
		switch r {
		case '%', '*':
			sb.WriteString(`.*`)
		case '_':
			sb.WriteString(`.`)
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	sb.WriteString(`\z`)
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

// RowSet is the minimal resultset surface the app needs. A builtin engine wraps
// its *sql.Rows in sqlRowSet (which adds ColTypes); a plugin returns a
// materializedRowSet built from the rows it shipped over the wire. Both satisfy
// this, so the app's query and catalog code never branches on the backend.
type RowSet interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
	Close() error
	Columns() []string
	ColTypes() []string
}

// sqlRowSet adapts *sql.Rows to RowSet by adding ColTypes and re-shaping
// Columns (whose driver signature carries an error) to the plain []string the
// interface wants.
type sqlRowSet struct {
	*sql.Rows
}

func (s sqlRowSet) Columns() []string {
	c, _ := s.Rows.Columns()
	return c
}

func (s sqlRowSet) ColTypes() []string {
	cts, err := s.Rows.ColumnTypes()
	if err != nil {
		return nil
	}
	out := make([]string, len(cts))
	for i, ct := range cts {
		if ct != nil {
			out[i] = ct.DatabaseTypeName()
		}
	}
	return out
}

// ExecResult is the outcome of a non-query statement.
type ExecResult struct {
	Affected     int64
	LastInsertID int64
}

// Engine executes SQL against one open connection. A connection may own several
// Engines (the main handle plus per-database pools), so an Engine is one pool,
// not the whole Conn.
type Engine interface {
	Query(ctx context.Context, query string, args ...interface{}) (RowSet, error)
	Exec(ctx context.Context, query string, args ...interface{}) (ExecResult, error)
	Begin(ctx context.Context) (Tx, error)
	Ping(ctx context.Context) error
	Close() error
}

// Tx is one transaction on an Engine.
type Tx interface {
	Exec(ctx context.Context, query string, args ...interface{}) (int64, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// convertAssign copies src into the pointer dest with the same coercion
// database/sql applies in Rows.Scan, restricted to the driver value shapes this
// app sees (string, []byte, int64, float64, bool, time.Time, nil). A plugin
// ships values as plain JSON, so the client re-materializes the exact Go type
// and then funnels it through here - the same path *sql.Rows.Scan takes for the
// in-process engines, so both backends scan identically.
func convertAssign(dest, src interface{}) error {
	switch d := dest.(type) {
	case *interface{}:
		*d = src
		return nil
	case *string:
		s, err := asString(src)
		if err != nil {
			return err
		}
		*d = s
		return nil
	case *[]byte:
		switch v := src.(type) {
		case nil:
			*d = nil
		case []byte:
			*d = append([]byte(nil), v...)
		case string:
			*d = []byte(v)
		default:
			return fmt.Errorf("cannot assign %T to []byte", src)
		}
		return nil
	case *bool:
		switch v := src.(type) {
		case bool:
			*d = v
		case int64:
			*d = v != 0
		case []byte:
			b, err := parseBool(string(v))
			if err != nil {
				return err
			}
			*d = b
		case string:
			b, err := parseBool(v)
			if err != nil {
				return err
			}
			*d = b
		default:
			return fmt.Errorf("cannot assign %T to bool", src)
		}
		return nil
	case *time.Time:
		switch v := src.(type) {
		case time.Time:
			*d = v
		case string:
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return fmt.Errorf("cannot parse %q as time: %w", v, err)
			}
			*d = t
		default:
			return fmt.Errorf("cannot assign %T to time.Time", src)
		}
		return nil
	}

	// Numeric destinations, by reflection so *int, *int64, *float64, *uint...
	// all land.
	dp := reflect.ValueOf(dest)
	if dp.Kind() != reflect.Ptr {
		return fmt.Errorf("dest not a pointer: %T", dest)
	}
	dv := dp.Elem()
	switch dv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := asInt64(src)
		if err != nil {
			return err
		}
		if dv.OverflowInt(n) {
			return fmt.Errorf("value %d overflows %s", n, dv.Type())
		}
		dv.SetInt(n)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := asInt64(src)
		if err != nil {
			return err
		}
		if n < 0 || dv.OverflowUint(uint64(n)) {
			return fmt.Errorf("value %d overflows %s", n, dv.Type())
		}
		dv.SetUint(uint64(n))
		return nil
	case reflect.Float32, reflect.Float64:
		f, err := asFloat64(src)
		if err != nil {
			return err
		}
		if dv.OverflowFloat(f) {
			return fmt.Errorf("value %v overflows %s", f, dv.Type())
		}
		dv.SetFloat(f)
		return nil
	}
	return fmt.Errorf("unsupported dest type %T", dest)
}

func asString(src interface{}) (string, error) {
	switch v := src.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case int64:
		return fmt.Sprintf("%d", v), nil
	case float64:
		return trimFloat(v), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case time.Time:
		return v.Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("cannot render %T as string", src)
}

func asInt64(src interface{}) (int64, error) {
	switch v := src.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case string:
		return parseInt(v)
	case []byte:
		return parseInt(string(v))
	}
	return 0, fmt.Errorf("cannot read %T as int64", src)
}

func asFloat64(src interface{}) (float64, error) {
	switch v := src.(type) {
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	case int:
		return float64(v), nil
	case string:
		return parseFloat(v)
	case []byte:
		return parseFloat(string(v))
	}
	return 0, fmt.Errorf("cannot read %T as float64", src)
}

func trimFloat(f float64) string {
	if f == math.Trunc(f) && !math.IsInf(f, 0) && math.Abs(f) < 1e15 {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%v", f)
}

// materializedRowSet adapts rows that arrived over the wire (already decoded
// into Go values) to the RowSet the app scans. It is what lets the plugin path
// reuse the exact same scan code as the in-process path.
type materializedRowSet struct {
	cols  []string
	types []string
	rows  [][]interface{}
	pos   int
	err   error
}

func (m *materializedRowSet) Columns() []string  { return m.cols }
func (m *materializedRowSet) ColTypes() []string { return m.types }

func (m *materializedRowSet) Next() bool {
	if m.pos >= len(m.rows) {
		return false
	}
	m.pos++
	return true
}

func (m *materializedRowSet) Scan(dest ...interface{}) error {
	row := m.rows[m.pos-1]
	if len(dest) != len(row) {
		return fmt.Errorf("scan wants %d values, row has %d", len(dest), len(row))
	}
	for i, d := range dest {
		if err := convertAssign(d, row[i]); err != nil {
			return err
		}
	}
	return nil
}

func (m *materializedRowSet) Err() error   { return m.err }
func (m *materializedRowSet) Close() error { return nil }

// ensure both backends satisfy RowSet at compile time.
var _ RowSet = sqlRowSet{}
var _ RowSet = (*materializedRowSet)(nil)
