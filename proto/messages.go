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
	// Done marks the terminal frame of a streaming reply (MethodQueryStream).
	// Every non-streaming reply is a single frame and leaves it false.
	Done bool `json:"done,omitempty"`
}

// Method names.
const (
	MethodMeta     = "meta"
	MethodOpen     = "open"
	MethodClose    = "close"
	MethodTables   = "tables"
	MethodColumns  = "columns"
	MethodQuery    = "query"
	MethodBrowse   = "browse"
	MethodCreateDB = "createDatabase"

	// DML methods let the host edit rows through a plugin the same way the
	// built-in MySQL driver can. They are additive: deployed plugins that
	// predate them simply answer "unknown method", so older binaries keep
	// working (the grid just stays read-only for those connections).
	MethodUpdate     = "update"
	MethodInsert     = "insert"
	MethodDelete     = "delete"
	MethodApplyBatch = "applyBatch"

	// Transaction methods drive a manual transaction on a connection
	// (TablePlus "Begin Transaction" mode). They are additive: deployed
	// plugins that predate them simply answer "unknown method", so older
	// binaries keep working (those connections just report no transaction
	// support).
	MethodBegin    = "begin"
	MethodCommit   = "commit"
	MethodRollback = "rollback"

	// MethodQueryStream streams a statement's result in batches so a large
	// result set is fetched (and can be drawn) incrementally with bounded
	// memory, instead of materialising every row in one frame. Additive like
	// the rest: an older plugin answers "unknown method" and the host falls
	// back to the non-streaming Query.
	MethodQueryStream = "queryStream"

	// MethodMetrics asks the plugin for a curated set of read-only server
	// metrics (connections, uptime, cache hit ratio, ...). Additive: an older
	// plugin answers "unknown method" and the connection simply reports no
	// metrics.
	MethodMetrics = "metrics"
)

// DefaultMarker is the sentinel *string value meaning "write the SQL keyword
// DEFAULT" rather than a literal (TablePlus "Set Value → DEFAULT"). It must
// match the host-side constant in db/mysql.go so the value round-trips
// unchanged through the protocol.
const DefaultMarker = "\x00DEFAULT"

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

// CreateDBParams creates a new database/schema on the server. Charset/collation
// are dialect-specific and optional; the plugin validates and quotes them.
type CreateDBParams struct {
	ConnID    string `json:"connId"`
	Name      string `json:"name"`
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
}

// DMLRow pairs a primary key with the changed columns for one UPDATE.
type DMLRow struct {
	PK      map[string]*string `json:"pk"`
	Changes map[string]*string `json:"changes"`
}

// DMLSingleParams carries one UPDATE/INSERT/DELETE operation. Exactly one of
// Changes (update) or Values (insert) is populated; PK identifies the target
// row for update/delete. nil values mean SQL NULL; the DefaultMarker sentinel
// means the SQL keyword DEFAULT.
type DMLSingleParams struct {
	ConnID  string             `json:"connId"`
	Table   string             `json:"table"`
	PK      map[string]*string `json:"pk,omitempty"`
	Changes map[string]*string `json:"changes,omitempty"`
	Values  map[string]*string `json:"values,omitempty"`
}

// DMLBatchParams carries a whole commit: row edits, deletions, inserts and an
// optional list of structural DDL. The plugin applies them atomically in one
// transaction.
type DMLBatchParams struct {
	ConnID  string               `json:"connId"`
	Table   string               `json:"table"`
	Updates []DMLRow             `json:"updates,omitempty"`
	Deletes []map[string]*string `json:"deletes,omitempty"`
	Inserts []map[string]*string `json:"inserts,omitempty"`
	DDL     []string             `json:"ddl,omitempty"`
}

// DMLResult reports how many rows a DML operation touched.
type DMLResult struct {
	RowsAffected int64 `json:"rowsAffected"`
}

// TxParams identifies the connection for a begin/commit/rollback call.
type TxParams struct {
	ConnID string `json:"connId"`
}

// QueryStreamParams asks the plugin to stream a statement's result in batches.
// Batch is the maximum number of rows per frame (0 → a sensible default).
type QueryStreamParams struct {
	ConnID string `json:"connId"`
	SQL    string `json:"sql"`
	Batch  int    `json:"batch,omitempty"`
}

// StreamFrame is one frame of a streaming reply. Data frames carry the column
// metadata on the first frame (Columns) and up to Batch rows (Rows); the
// terminal frame carries the aggregate RowsAffected and DurationMS. All frames
// of a stream share the request ID; the terminal one sets Response.Done.
type StreamFrame struct {
	Columns      []ColumnInfo `json:"columns,omitempty"`
	Rows         [][]*string  `json:"rows,omitempty"`
	RowsAffected int64        `json:"rowsAffected,omitempty"`
	DurationMS   int64        `json:"durationMs,omitempty"`
}

// MetricsParams asks for a connection's server metrics.
type MetricsParams struct {
	ConnID string `json:"connId"`
}

// Metric is one labelled server metric value (already formatted for display).
type Metric struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Group string `json:"group,omitempty"` // optional section heading
}

// MetricsResult answers MethodMetrics.
type MetricsResult struct {
	Metrics []Metric `json:"metrics"`
}
