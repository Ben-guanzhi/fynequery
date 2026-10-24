package db

import "github.com/dbcore/dbcore"

// IndexLister is optionally implemented by Session types whose engine is served
// by a dbcore.SQLDriver. The Indexes pane then renders structured kernel
// metadata instead of parsing engine-specific catalog rows (information_schema,
// PRAGMA, …) in the UI layer.
//
// schema follows the kernel convention: MySQL passes the database name,
// PostgreSQL the schema, SQLite ignores it.
type IndexLister interface {
	ListIndexes(schema, table string) ([]dbcore.IndexInfo, error)
}
