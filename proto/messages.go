// Package proto defines the wire protocol between fynequery and its
// database plugin processes. Both sides import this package, so any change
// here must stay backwards compatible with deployed plugins.
//
// Transport: the plugin is a child process reading newline-delimited JSON
// requests on stdin and writing newline-delimited JSON responses on stdout.
// On startup the plugin first writes a banner line identifying itself.
package proto

import "encoding/json"

// Protocol values for the startup banner.
const (
	ProtocolName    = "fynequery-plugin"
	ProtocolVersion = 1
)

// Banner is the first line a plugin writes on stdout so the host can verify
// it speaks this protocol before sending requests.
type Banner struct {
	Proto   string `json:"proto"`
	Version int    `json:"version"`
	Code    string `json:"code"` // driver code, e.g. "postgres"
	Name    string `json:"name"` // display name, e.g. "PostgreSQL"
}

// Request is one host→plugin call.
type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is one plugin→host reply, matching a Request by ID.
type Response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Method names.
const (
	MethodMeta    = "meta"
	MethodOpen    = "open"
	MethodClose   = "close"
	MethodTables  = "tables"
	MethodColumns = "columns"
	MethodQuery   = "query"
	MethodBrowse  = "browse"
)

// ConnectParams carries everything needed to open one database connection.
type ConnectParams struct {
	Name     string            `json:"name"`
	Host     string            `json:"host"`
	Port     string            `json:"port"`
	User     string            `json:"user"`
	Password string            `json:"password"`
	Database string            `json:"database"`
	Options  map[string]string `json:"options,omitempty"` // driver-specific extras (sslmode, service name...)
}

// MetaResult answers MethodMeta.
type MetaResult struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	DefaultPort string `json:"defaultPort"`
}

// OpenResult answers MethodOpen.
type OpenResult struct {
	ConnID string `json:"connId"`
	Server string `json:"server,omitempty"` // server version string, shown in status
}

// CloseParams identifies a connection to drop.
type CloseParams struct {
	ConnID string `json:"connId"`
}

// TablesParams asks for the table/view inventory of a connection.
type TablesParams struct {
	ConnID string `json:"connId"`
}

// ObjectInfo is one sidebar entry. Rows is an approximate count when the
// server cannot report it cheaply (approxRows=true).
type ObjectInfo struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // "table" | "view"
	Rows       int64  `json:"rows"`
	ApproxRows bool   `json:"approxRows"`
}

// TablesResult answers MethodTables.
type TablesResult struct {
	Objects []ObjectInfo `json:"objects"`
}

// ColumnsParams asks for the column metadata of one table or view.
type ColumnsParams struct {
	ConnID string `json:"connId"`
	Table  string `json:"table"`
}

// ColumnInfo mirrors one grid/structure column.
type ColumnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Key      string `json:"key,omitempty"` // "PK" when part of the primary key
}

// ColumnsResult answers MethodColumns.
type ColumnsResult struct {
	Columns []ColumnInfo `json:"columns"`
}

// QueryParams runs one SQL statement.
type QueryParams struct {
	ConnID  string `json:"connId"`
	SQL     string `json:"sql"`
	MaxRows int    `json:"maxRows"`
}

// QueryResult answers MethodQuery. Rows cells are strings; a JSON null is a
// SQL NULL. RowsAffected is set for non-SELECT statements.
type QueryResult struct {
	Columns      []ColumnInfo `json:"columns"`
	Rows         [][]*string  `json:"rows"`
	RowsAffected int64        `json:"rowsAffected,omitempty"`
	DurationMS   int64        `json:"durationMs"`
}

// BrowseParams fetches the first rows of one table (dialect-specific paging).
type BrowseParams struct {
	ConnID string `json:"connId"`
	Table  string `json:"table"`
	Limit  int    `json:"limit"`
}
