// Package proto defines the wire protocol between fynequery and its
// database plugin processes.
//
// The protocol itself now lives in the kernel (github.com/dbcore/dbcore/
// plugindriver), so a plugin written for one client works with the other; this
// package is the fynequery-facing view of it: the method names, the payloads,
// and the conversions between tagged cells and the nullable strings the Fyne UI
// works with.
//
// Transport: the plugin is a child process reading newline-delimited JSON
// requests on stdin and writing newline-delimited JSON responses on stdout. On
// startup the plugin first writes a banner line identifying itself.
package proto

// Method names. These are the names the deployed plugin binaries answer to, so
// they do not change; the kernel's equivalent names are listed in kernel.go for
// a host that speaks the kernel protocol.
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

	// MethodQueryStream streams a statement's result in batches. Additive.
	MethodQueryStream = "queryStream"

	// MethodMetrics returns a curated set of read-only server metrics. Additive.
	MethodMetrics = "metrics"
)
