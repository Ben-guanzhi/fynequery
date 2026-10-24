// fq-plugin-postgres: PostgreSQL support for fynequery, delivered as an
// external plugin process speaking the fynequery plugin protocol.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver

	"github.com/dbcore/dbcore/postgres"
	"github.com/dbcore/dbcore/sqltext"

	"fynequery/plugins/internal/pluginkit"
	"fynequery/proto"
)

const pluginVersion = "1.0.0"

type pgDialect struct{}

func (pgDialect) Code() string        { return "postgres" }
func (pgDialect) Name() string        { return "PostgreSQL" }
func (pgDialect) Version() string     { return pluginVersion }
func (pgDialect) DefaultPort() string { return "5432" }

func (d pgDialect) Open(p proto.ConnectParams) (*sql.DB, string, error) {
	port := p.Port
	if port == "" {
		port = d.DefaultPort()
	}
	sslmode := p.Options["sslmode"]
	if sslmode == "" {
		sslmode = "prefer"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(p.User, p.Password),
		Host:   net.JoinHostPort(p.Host, port),
		Path:   p.Database,
	}
	q := u.Query()
	q.Set("sslmode", sslmode)
	u.RawQuery = q.Encode()

	handle, server, err := pluginkit.OpenVerify("pgx", u.String(), "SHOW server_version")
	if err != nil {
		return nil, "", fmt.Errorf("postgres %s: %w", u.Host, err)
	}
	if server != "" {
		server = "PostgreSQL " + server
	}
	return handle, server, nil
}

func (pgDialect) ListTables(db *sql.DB) ([]proto.ObjectInfo, error) {
	// The dbcore reader resolves current_schema() when the schema is empty and
	// reports row estimates from pg_class.reltuples.
	found, err := postgres.NewFromDB(db).ListTables(context.Background(), "")
	if err != nil {
		return nil, err
	}
	out := make([]proto.ObjectInfo, 0, len(found))
	for _, t := range found {
		kind := "table"
		if strings.Contains(strings.ToUpper(t.Type), "VIEW") {
			kind = "view" // fold "materialized_view" too: the sidebar only knows table/view
		}
		out = append(out, proto.ObjectInfo{
			Name: t.Name, Kind: kind, Rows: t.Rows, ApproxRows: t.ApproxRows,
		})
	}
	return out, nil
}

func (pgDialect) ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error) {
	// The dbcore reader resolves current_schema() itself and returns comments
	// and primary-key flags in the shared shape.
	found, err := postgres.NewFromDB(db).ListColumns(context.Background(), "", table)
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

func (pgDialect) BrowseSQL(table string, limit int) string {
	return fmt.Sprintf("SELECT * FROM %s LIMIT %d", quotePG(table), limit)
}

// QuoteIdent quotes a PostgreSQL identifier with double quotes (escaping
// embedded quotes). Used by the shared DML builders in pluginkit.
func (pgDialect) QuoteIdent(name string) string { return quotePG(name) }

// Placeholder renders PostgreSQL's positional parameter ($1, $2, ...).
func (pgDialect) Placeholder(i int) string { return fmt.Sprintf("$%d", i) }

// Metrics returns curated read-only PostgreSQL server metrics.
func (pgDialect) Metrics(db *sql.DB) ([]proto.Metric, error) {
	const q = `
		SELECT 'Version', current_setting('server_version')
		UNION ALL SELECT 'Database', current_database()
		UNION ALL SELECT 'Active connections', count(*)::text FROM pg_stat_activity
		UNION ALL SELECT 'Max connections', current_setting('max_connections')
		UNION ALL SELECT 'Cache hit ratio',
			round(100.0*blks_hit/NULLIF(blks_hit+blks_read,0),1)::text || '%'
			FROM pg_stat_database WHERE datname = current_database()`
	return pluginkit.KVMetrics(db, "PostgreSQL", q)
}

func quotePG(name string) string {
	if q, err := sqltext.QuoteIdent("postgres", name); err == nil {
		return q
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// CreateDatabase creates a new PostgreSQL database. CREATE DATABASE must not run
// inside a transaction, so it is executed on a dedicated connection.
func (pgDialect) CreateDatabase(db *sql.DB, name, _, _ string) error {
	q := "CREATE DATABASE " + quotePG(name)
	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("postgres create database: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), q); err != nil {
		return fmt.Errorf("postgres create database %q: %w", name, err)
	}
	return nil
}

func main() { pluginkit.Main(pgDialect{}) }
