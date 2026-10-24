// fq-plugin-oracle: Oracle support for fynequery, delivered as an external
// plugin process speaking the fynequery plugin protocol. Uses the pure-Go
// go-ora driver, so no Oracle Instant Client installation is required.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	goora "github.com/sijms/go-ora/v2"

	"github.com/dbcore/dbcore/oracle"
	"github.com/dbcore/dbcore/sqltext"

	"fynequery/plugins/internal/pluginkit"
	"fynequery/proto"
)

const pluginVersion = "1.0.0"

type oraDialect struct{}

func (oraDialect) Code() string        { return "oracle" }
func (oraDialect) Name() string        { return "Oracle" }
func (oraDialect) Version() string     { return pluginVersion }
func (oraDialect) DefaultPort() string { return "1521" }

func (d oraDialect) Open(p proto.ConnectParams) (*sql.DB, string, error) {
	port := p.Port
	if port == "" {
		port = d.DefaultPort()
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, "", fmt.Errorf("invalid port %q", port)
	}
	// For Oracle the "database" field is the service name.
	dsn := goora.BuildUrl(p.Host, portNum, p.Database, p.User, p.Password, nil)
	handle, server, err := pluginkit.OpenVerify("oracle", dsn,
		`SELECT banner FROM v$version WHERE ROWNUM = 1`)
	if err != nil {
		return nil, "", fmt.Errorf("oracle %s:%s/%s: %w", p.Host, port, p.Database, err)
	}
	return handle, server, nil
}

func (oraDialect) ListTables(db *sql.DB) ([]proto.ObjectInfo, error) {
	// An empty schema makes the dbcore reader use the session's current schema;
	// row estimates come from ALL_TABLES.NUM_ROWS.
	found, err := oracle.NewFromDB(db).ListTables(context.Background(), "")
	if err != nil {
		return nil, err
	}
	out := make([]proto.ObjectInfo, 0, len(found))
	for _, t := range found {
		out = append(out, proto.ObjectInfo{
			Name: t.Name, Kind: t.Type, Rows: t.Rows, ApproxRows: t.ApproxRows,
		})
	}
	return out, nil
}

func (oraDialect) ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error) {
	// An empty schema makes the dbcore reader use the session's current schema.
	found, err := oracle.NewFromDB(db).ListColumns(context.Background(), "", table)
	if err != nil {
		return nil, err
	}
	out := make([]proto.ColumnInfo, 0, len(found))
	for _, c := range found {
		col := proto.ColumnInfo{Name: c.Name, Type: c.Type, Nullable: c.Nullable}
		if c.IsPrimaryKey {
			col.Key = "PK"
		}
		out = append(out, col)
	}
	return out, nil
}

func (oraDialect) BrowseSQL(table string, limit int) string {
	return fmt.Sprintf("SELECT * FROM %s FETCH FIRST %d ROWS ONLY", quoteOra(table), limit)
}

// QuoteIdent quotes an Oracle identifier with double quotes (escaping embedded
// quotes). Used by the shared DML builders in pluginkit.
func (oraDialect) QuoteIdent(name string) string { return quoteOra(name) }

// Placeholder renders Oracle's positional parameter (:1, :2, ...).
func (oraDialect) Placeholder(i int) string { return fmt.Sprintf(":%d", i) }

// Metrics returns curated read-only Oracle server metrics.
func (oraDialect) Metrics(db *sql.DB) ([]proto.Metric, error) {
	const q = `
		SELECT 'Version', banner FROM v$version WHERE ROWNUM = 1
		UNION ALL SELECT 'Sessions', TO_CHAR(value) FROM v$sysstat WHERE name = 'session count'
		UNION ALL SELECT 'User commits', TO_CHAR(value) FROM v$sysstat WHERE name = 'user commits'
		UNION ALL SELECT 'User rollbacks', TO_CHAR(value) FROM v$sysstat WHERE name = 'user rollbacks'
		UNION ALL SELECT 'Physical reads', TO_CHAR(value) FROM v$sysstat WHERE name = 'physical reads'`
	return pluginkit.KVMetrics(db, "Oracle", q)
}

// CreateDatabase is intentionally unsupported: Oracle databases are created via
// DBCA or DBA tooling, not from an ordinary client session. We report this
// clearly so the UI can surface a helpful message rather than a confusing
// driver failure.
func (oraDialect) CreateDatabase(db *sql.DB, name, charset, collation string) error {
	return fmt.Errorf("oracle does not support creating databases from a client session; use DBCA or your DBA tooling")
}

func quoteOra(name string) string {
	if q, err := sqltext.QuoteIdent("oracle", name); err == nil {
		return q
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func main() { pluginkit.Main(oraDialect{}) }
