// Package pluginkit is the shared skeleton for fynequery plugin
// executables: connection bookkeeping and the protocol serve loop. A plugin
// main supplies a Dialect (driver-specific SQL) and is otherwise one call.
package pluginkit

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"fynequery/internal/sqlkit"
	"fynequery/proto"
)

// Dialect supplies the driver-specific behavior of a plugin.
type Dialect interface {
	Code() string        // "postgres", "mssql", "oracle"...
	Name() string        // "PostgreSQL", "SQL Server"...
	Version() string     // plugin version string
	DefaultPort() string // "5432"...
	// Open dials the server, verifies the connection (Ping) and reports a
	// server identification string.
	Open(p proto.ConnectParams) (*sql.DB, string, error)
	// ListTables returns tables and views of the connected database.
	ListTables(db *sql.DB) ([]proto.ObjectInfo, error)
	// ListColumns returns column metadata; Key="PK" marks primary keys.
	ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error)
	// BrowseSQL builds a "first N rows of table" statement in the dialect.
	BrowseSQL(table string, limit int) string
}

// Main runs the plugin until the host closes stdin. Never returns.
func Main(d Dialect) {
	h := newHandler(d)
	banner := proto.Banner{
		Proto:   proto.ProtocolName,
		Version: proto.ProtocolVersion,
		Code:    d.Code(),
		Name:    d.Name(),
	}
	if err := proto.Serve(h, banner); err != nil {
		log.Fatalf("serve: %v", err)
	}
	// stdin closed: host is gone, exit quietly.
	os.Exit(0)
}

// OpenVerify opens a database/sql connection, pings it with a timeout and,
// when versionQuery is not empty, reads one scalar for the server banner.
// It replaces the Open boilerplate shared by every dialect.
func OpenVerify(driverName, dsn, versionQuery string) (*sql.DB, string, error) {
	handle, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		handle.Close()
		return nil, "", err
	}
	if versionQuery == "" {
		return handle, "", nil
	}
	var v interface{}
	if err := handle.QueryRowContext(ctx, versionQuery).Scan(&v); err != nil {
		return handle, "", nil // server string is best effort
	}
	if s := sqlkit.FormatValue(v); s != nil {
		return handle, *s, nil
	}
	return handle, "", nil
}

// Atoi64 converts a scanned numeric-ish value (int64, float64, string, []byte)
// to int64; -1 when nil or unparseable (drivers disagree on NUMBER types).
func Atoi64(v interface{}) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case []byte:
		if n, err := strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64); err == nil {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n
		}
	}
	return -1
}

type handler struct {
	dialect Dialect
	mu      sync.Mutex
	conns   map[string]*sql.DB
	server  map[string]string
	next    int
}

// newHandler builds a handler with every map initialized; Open writes to both
// conns and server, so a missing map would panic on the first success.
func newHandler(d Dialect) *handler {
	return &handler{
		dialect: d,
		conns:   map[string]*sql.DB{},
		server:  map[string]string{},
	}
}

func (h *handler) Meta() proto.MetaResult {
	return proto.MetaResult{
		Code:        h.dialect.Code(),
		Name:        h.dialect.Name(),
		Version:     h.dialect.Version(),
		DefaultPort: h.dialect.DefaultPort(),
	}
}

func (h *handler) Open(p proto.ConnectParams) (proto.OpenResult, error) {
	handle, server, err := h.dialect.Open(p)
	if err != nil {
		return proto.OpenResult{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := fmt.Sprintf("c%d", h.next)
	h.conns[id] = handle
	h.server[id] = server
	return proto.OpenResult{ConnID: id, Server: server}, nil
}

func (h *handler) Close(p proto.CloseParams) error {
	h.mu.Lock()
	handle, ok := h.conns[p.ConnID]
	delete(h.conns, p.ConnID)
	delete(h.server, p.ConnID)
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown connection %q", p.ConnID)
	}
	return handle.Close()
}

func (h *handler) conn(id string) (*sql.DB, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	handle, ok := h.conns[id]
	if !ok {
		return nil, fmt.Errorf("unknown connection %q (closed?)", id)
	}
	return handle, nil
}

func (h *handler) Tables(p proto.TablesParams) (proto.TablesResult, error) {
	handle, err := h.conn(p.ConnID)
	if err != nil {
		return proto.TablesResult{}, err
	}
	objs, err := h.dialect.ListTables(handle)
	return proto.TablesResult{Objects: objs}, err
}

func (h *handler) Columns(p proto.ColumnsParams) (proto.ColumnsResult, error) {
	handle, err := h.conn(p.ConnID)
	if err != nil {
		return proto.ColumnsResult{}, err
	}
	cols, err := h.dialect.ListColumns(handle, p.Table)
	return proto.ColumnsResult{Columns: cols}, err
}

func (h *handler) Query(p proto.QueryParams) (proto.QueryResult, error) {
	handle, err := h.conn(p.ConnID)
	if err != nil {
		return proto.QueryResult{}, err
	}
	return sqlkit.Run(handle, p.SQL, p.MaxRows)
}

func (h *handler) Browse(p proto.BrowseParams) (proto.QueryResult, error) {
	handle, err := h.conn(p.ConnID)
	if err != nil {
		return proto.QueryResult{}, err
	}
	if p.Limit <= 0 {
		p.Limit = 1000
	}
	return sqlkit.Run(handle, h.dialect.BrowseSQL(p.Table, p.Limit), p.Limit)
}
