package db

import (
	"context"
	"regexp"
	"strings"
)

// columns lists column metadata for a table; the per-engine queries and scans
// live on the dialect (dialect.go).
func (c *Store) Columns(ctx context.Context, id, database, schema, table string) ([]ColumnInfo, error) {
	conn, err := c.OpenConn(id)
	if err != nil {
		return nil, err
	}
	if schema == "" {
		schema = ResolveSchema(conn.Config.Type, database, schema)
	}
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		return nil, err
	}
	return d.columns(ctx, c, conn, database, schema, table)
}

// tableIndexes reads one table's index list via the dialect.
func (c *Store) TableIndexes(ctx context.Context, connID, database, schema, table string) ([]IndexInfo, error) {
	conn, err := c.OpenConn(connID)
	if err != nil {
		return nil, err
	}
	if schema == "" {
		schema = ResolveSchema(conn.Config.Type, database, schema)
	}
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		return nil, err
	}
	return d.indexes(ctx, c, conn, database, schema, table)
}

// scanStrings runs a single-string-column query and collects results.
func scanStrings(ctx context.Context, conn *Conn, query string) ([]string, error) {
	return collectRows(ctx, conn, query, nil, scanString)
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
