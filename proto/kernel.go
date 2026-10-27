package proto

// The wire format is the kernel's (github.com/dbcore/dbcore/plugindriver), so a
// plugin written for one client works with the other.
//
// What was fynequery's own protocol and what is now the kernel's:
//
//   - the transport (newline-delimited JSON, a banner first, one response per
//     request id) was already the same shape; the banner now carries the kernel's
//     fields, and the old "fynequery-plugin" name is still accepted so a plugin
//     built before the move keeps working;
//   - the request/response envelopes are the kernel's;
//   - the cell representation is the kernel's tagged form, which fixes a real
//     problem: the old one sent every cell as a string, so a NULL and the empty
//     string were the same thing on the wire.
//
// The method names stay as they were: they are part of the deployed plugin
// binaries' contract, and the kernel's connection-oriented names are the
// equivalent calls (see the kernel's connproto.go).

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/dbcore/dbcore/plugindriver"
)

// The protocol name and version this package speaks. ProtocolName stays
// "fynequery-plugin" so the plugins already deployed are still recognised; the
// banner also carries the kernel's name, so a kernel host can accept either.
const (
	ProtocolName    = plugindriver.ProtocolName
	ProtocolVersion = plugindriver.ProtocolVersion
	// LegacyProtocolName is what the protocol was called before it moved into
	// the kernel. A host still accepts it.
	LegacyProtocolName = "fynequery-plugin"
)

// The request and response envelopes are the kernel's: the framing (one id per
// request, one reply per id, an error field) is what the transport needs, and
// there is no reason for it to differ between clients.
type (
	Request  = plugindriver.Request
	Response = plugindriver.Response
)

// Banner is the first line a plugin writes. It keeps fynequery's field names
// ("code" is the engine name) and carries the kernel's name too, so a kernel
// host can tell what it launched.
type Banner struct {
	Proto   string `json:"proto"`
	Version int    `json:"version"`
	// Code is the engine this plugin implements ("postgres", "mysql", ...).
	Code string `json:"code"`
	// Name is the display name.
	Name string `json:"name"`
	// Kernel names the engine in the kernel's vocabulary, so a kernel host does
	// not have to translate.
	Kernel string `json:"kind,omitempty"`
}

// KernelProtocolName is the name the kernel uses for this protocol.
const KernelProtocolName = plugindriver.ProtocolName

// AcceptsBanner reports whether a banner names a protocol this host speaks.
// Both names are accepted: the kernel's, and the legacy fynequery one, so a
// plugin built before the move is not orphaned by it.
func AcceptsBanner(banner Banner) error {
	switch banner.Proto {
	case ProtocolName, LegacyProtocolName:
		return nil
	}
	return fmt.Errorf("plugin speaks %q, want %q", banner.Proto, ProtocolName)
}

// KernelBanner builds the banner a plugin writes. It names the kernel protocol
// so a kernel host accepts it, and carries the legacy name in the version field
// only as a marker.
func KernelBanner(code, name, version string) Banner {
	return Banner{
		Proto:   ProtocolName,
		Version: ProtocolVersion,
		Code:    code,
		Name:    name,
		Kernel:  code,
	}
}

// The connection-oriented payloads are the kernel's.
type (
	ConnectParams = plugindriver.ConnectParams
	OpenResult    = plugindriver.OpenResult
	ConnObject    = plugindriver.ConnObject
	ConnColumn    = plugindriver.ConnColumn
	ConnMetric    = plugindriver.ConnMetric
	ConnDMLRow    = plugindriver.ConnDMLRow
	CellPayload   = plugindriver.CellPayload
)

// The kernel's method names for the connection-oriented calls. They are the same
// operations the fynequery protocol has always had, under the kernel's names.
const (
	KernelMethodMeta        = plugindriver.MethodOpenMeta
	KernelMethodOpen        = plugindriver.MethodOpen
	KernelMethodClose       = plugindriver.MethodCloseConn
	KernelMethodTables      = plugindriver.MethodListConnTables
	KernelMethodColumns     = plugindriver.MethodListConnColumns
	KernelMethodQuery       = plugindriver.MethodConnQuery
	KernelMethodBrowse      = plugindriver.MethodConnBrowse
	KernelMethodCreateDB    = plugindriver.MethodConnCreateDB
	KernelMethodUpdate      = plugindriver.MethodConnUpdate
	KernelMethodInsert      = plugindriver.MethodConnInsert
	KernelMethodDelete      = plugindriver.MethodConnDelete
	KernelMethodApplyBatch  = plugindriver.MethodConnApplyBatch
	KernelMethodBegin       = plugindriver.MethodConnBegin
	KernelMethodCommit      = plugindriver.MethodConnCommit
	KernelMethodRollback    = plugindriver.MethodConnRollback
	KernelMethodQueryStream = plugindriver.MethodConnQueryStream
	KernelMethodMetrics     = plugindriver.MethodConnMetrics
	KernelMethodCancel      = plugindriver.MethodConnCancel
)

// The result payloads keep the fynequery shape (they are what the plugins
// answer), but the cells are the kernel's tagged form.
type (
	// ObjectInfo is one sidebar entry. Rows is an approximate count when the
	// server cannot report it cheaply (approxRows=true).
	ObjectInfo struct {
		Name       string `json:"name"`
		Kind       string `json:"kind"` // "table" | "view"
		Rows       int64  `json:"rows"`
		ApproxRows bool   `json:"approxRows"`
	}

	// ColumnInfo mirrors one grid/structure column.
	ColumnInfo struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Nullable bool   `json:"nullable"`
		Key      string `json:"key,omitempty"` // "PK" when part of the primary key
	}

	// QueryResult answers a query. The rows carry the kernel's tagged cells, so
	// a value keeps its kind on the way through the pipe: a bigint key arrives as
	// the integer it is, not as a string that later becomes a float.
	//
	// The UI still works with nullable strings (it shows text, and it edits
	// text); the conversion happens once, where the result enters the UI.
	QueryResult struct {
		Columns      []ColumnInfo    `json:"columns"`
		Rows         [][]CellPayload `json:"rows"`
		RowsAffected int64           `json:"rowsAffected,omitempty"`
		DurationMS   int64           `json:"durationMs"`
	}

	// StreamFrame is one frame of a streaming reply.
	StreamFrame struct {
		Columns      []ColumnInfo    `json:"columns,omitempty"`
		Rows         [][]CellPayload `json:"rows,omitempty"`
		RowsAffected int64           `json:"rowsAffected,omitempty"`
		DurationMS   int64           `json:"durationMs"`
	}

	// DMLRow pairs a primary key with the changed columns for one UPDATE.
	DMLRow struct {
		PK      map[string]CellPayload `json:"pk"`
		Changes map[string]CellPayload `json:"changes"`
	}

	// DMLResult reports how many rows a DML operation touched.
	DMLResult struct {
		RowsAffected int64 `json:"rowsAffected"`
	}

	// Metric is one labelled server metric value (already formatted for display).
	Metric struct {
		Label string `json:"label"`
		Value string `json:"value"`
		Group string `json:"group,omitempty"`
	}

	// MetricsResult answers the metrics call.
	MetricsResult struct {
		Metrics []Metric `json:"metrics"`
	}

	// MetaResult answers the meta call.
	MetaResult struct {
		Code        string `json:"code"`
		Name        string `json:"name"`
		Version     string `json:"version"`
		DefaultPort string `json:"defaultPort"`
	}
)

// The parameter payloads keep the fynequery shape.
type (
	// CloseParams identifies a connection to drop.
	CloseParams struct {
		ConnID string `json:"connId"`
	}

	// TablesParams asks for the table/view inventory of a connection.
	TablesParams struct {
		ConnID string `json:"connId"`
	}

	// TablesResult answers the tables call.
	TablesResult struct {
		Objects []ObjectInfo `json:"objects"`
	}

	// ColumnsParams asks for the column metadata of one table or view.
	ColumnsParams struct {
		ConnID string `json:"connId"`
		Table  string `json:"table"`
	}

	// ColumnsResult answers the columns call.
	ColumnsResult struct {
		Columns []ColumnInfo `json:"columns"`
	}

	// QueryParams runs one SQL statement.
	QueryParams struct {
		ConnID  string `json:"connId"`
		SQL     string `json:"sql"`
		MaxRows int    `json:"maxRows"`
	}

	// BrowseParams fetches the first rows of one table.
	BrowseParams struct {
		ConnID string `json:"connId"`
		Table  string `json:"table"`
		Limit  int    `json:"limit"`
	}

	// CreateDBParams creates a new database/schema on the server.
	CreateDBParams struct {
		ConnID    string `json:"connId"`
		Name      string `json:"name"`
		Charset   string `json:"charset,omitempty"`
		Collation string `json:"collation,omitempty"`
	}

	// DMLSingleParams carries one UPDATE/INSERT/DELETE operation.
	DMLSingleParams struct {
		ConnID  string                 `json:"connId"`
		Table   string                 `json:"table"`
		PK      map[string]CellPayload `json:"pk,omitempty"`
		Changes map[string]CellPayload `json:"changes,omitempty"`
		Values  map[string]CellPayload `json:"values,omitempty"`
	}

	// DMLBatchParams carries a whole commit.
	DMLBatchParams struct {
		ConnID  string                   `json:"connId"`
		Table   string                   `json:"table"`
		Updates []DMLRow                 `json:"updates,omitempty"`
		Deletes []map[string]CellPayload `json:"deletes,omitempty"`
		Inserts []map[string]CellPayload `json:"inserts,omitempty"`
		DDL     []string                 `json:"ddl,omitempty"`
	}

	// TxParams identifies the connection for a begin/commit/rollback call.
	TxParams struct {
		ConnID string `json:"connId"`
	}

	// CancelParams stops every statement running on one connection. The call is
	// served while the statement's own request is still in flight (each request
	// runs in its own goroutine), so the plugin can cancel the statement's
	// context and abort the database/sql call.
	CancelParams struct {
		ConnID string `json:"connId"`
	}

	// QueryStreamParams asks the plugin to stream a statement's result in batches.
	QueryStreamParams struct {
		ConnID string `json:"connId"`
		SQL    string `json:"sql"`
		Batch  int    `json:"batch,omitempty"`
	}

	// MetricsParams asks for a connection's server metrics.
	MetricsParams struct {
		ConnID string `json:"connId"`
	}
)

// DefaultMarker is the sentinel a DML value carries when the user wants the
// column's default rather than a value.
const DefaultMarker = "__DEFAULT__"

// StringRows renders the result's rows as the nullable strings the UI works
// with. The wire keeps the kinds; the UI shows text. A NULL stays nil, so it is
// not confused with the empty string.
func (q QueryResult) StringRows() [][]*string {
	out := make([][]*string, len(q.Rows))
	for i, row := range q.Rows {
		out[i] = CellsToStrings(row)
	}
	return out
}

// CellText is the text of one tagged cell. A NULL has no text at all — it is not
// the empty string, and the grid says so.
func CellText(cell CellPayload) string {
	switch cell.Kind {
	case "int":
		return strconv.FormatInt(cell.Int, 10)
	case "float":
		return strconv.FormatFloat(cell.Flt, 'g', -1, 64)
	case "text":
		return cell.Text
	case "bool":
		return strconv.FormatBool(cell.Bool)
	case "blob":
		return string(cell.Blob)
	case "time":
		return cell.Time
	case "decimal":
		// Exact decimal digits: never re-format them through a float, that is
		// the whole reason the kind exists.
		return cell.Text
	default:
		return ""
	}
}

// TextCell is a text cell, the shape the editable grid and the plugins used
// before the tagged form. It is still what the plugin code writes, so the
// conversion is one function.
func TextCell(s string) CellPayload { return CellPayload{Kind: "text", Text: s} }

// NullCell is a SQL NULL.
func NullCell() CellPayload { return CellPayload{Kind: "null"} }

// CellsToStrings renders tagged cells as the nullable strings the fynequery UI
// works with. A NULL stays nil; every other kind is its text form, so a number
// does not become "1" and a NULL does not become "".
func CellsToStrings(row []CellPayload) []*string {
	out := make([]*string, len(row))
	for i, cell := range row {
		if cell.Kind == "" || cell.Kind == "null" {
			continue
		}
		text := CellText(cell)
		out[i] = &text
	}
	return out
}

// StringsToCells renders nullable strings as tagged cells.
func StringsToCells(row []*string) []CellPayload {
	out := make([]CellPayload, len(row))
	for i, value := range row {
		if value == nil {
			out[i] = NullCell()
			continue
		}
		out[i] = TextCell(*value)
	}
	return out
}

// jsonNumber is used by the kernel's tagged form; kept here so the alias layer
// compiles against either.
var _ = json.Marshal
