// fq-plugin-mssql: SQL Server support for fynequery, delivered as an
// external plugin process speaking the fynequery plugin protocol.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strings"

	_ "github.com/microsoft/go-mssqldb" // registers the "sqlserver" driver

	"github.com/dbcore/dbcore/mssql"
	"github.com/dbcore/dbcore/sqltext"

	"fynequery/plugins/internal/pluginkit"
	"fynequery/proto"
)

const pluginVersion = "1.0.0"

type msDialect struct{}

func (msDialect) Code() string        { return "mssql" }
func (msDialect) Name() string        { return "SQL Server" }
func (msDialect) Version() string     { return pluginVersion }
func (msDialect) DefaultPort() string { return "1433" }

func (d msDialect) Open(p proto.ConnectParams) (*sql.DB, string, error) {
	port := p.Port
	if port == "" {
		port = d.DefaultPort()
	}
	// buildDSN assembles the sqlserver:// DSN, merging any extra parameters
	// (used for the legacy-TLS retry below).
	buildDSN := func(extra map[string]string) string {
		u := &url.URL{
			Scheme: "sqlserver",
			User:   url.UserPassword(p.User, p.Password),
			Host:   net.JoinHostPort(p.Host, port),
		}
		q := url.Values{}
		q.Set("database", p.Database)
		for k, v := range extra {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		return u.String()
	}

	handle, server, err := pluginkit.OpenVerify("sqlserver", buildDSN(nil),
		`SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(50))`)
	if err != nil && isLegacyTLSErr(err) {
		// Older SQL Server instances may only offer TLS 1.0, which Go's
		// client refuses by default (minimum TLS 1.2). Retry allowing TLS
		// 1.0 and skipping certificate validation, the usual posture for
		// self-signed SQL Server certificates.
		handle, server, err = pluginkit.OpenVerify("sqlserver", buildDSN(map[string]string{
			"tlsmin":                 "1.0",
			"TrustServerCertificate": "true",
		}), `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(50))`)
	}
	if err != nil {
		return nil, "", fmt.Errorf("sqlserver %s: %w", net.JoinHostPort(p.Host, port), err)
	}
	if server != "" {
		server = "SQL Server " + server
	}
	return handle, server, nil
}

// isLegacyTLSErr reports whether err looks like a TLS handshake/negotiation
// failure, the signal that the server only speaks an old protocol version or
// presents a certificate Go cannot validate.
func isLegacyTLSErr(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"tls", "handshake", "protocol version", "certificate", "x509", "remote error",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func (msDialect) ListTables(db *sql.DB) ([]proto.ObjectInfo, error) {
	// An empty schema makes the dbcore reader use the login's default schema;
	// row counts come from sys.partitions.
	found, err := mssql.NewFromDB(db).ListTables(context.Background(), "")
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

func (msDialect) ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error) {
	// An empty schema makes the dbcore reader use the login's default schema.
	found, err := mssql.NewFromDB(db).ListColumns(context.Background(), "", table)
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

func (msDialect) BrowseSQL(table string, limit int) string {
	// [] is not standard SQL but is the SQL Server quote character.
	quoted := strings.ReplaceAll(table, "]", "]]")
	return fmt.Sprintf("SELECT TOP (%d) * FROM [%s]", limit, quoted)
}

// QuoteIdent quotes a SQL Server identifier with square brackets (escaping
// embedded brackets). Used by the shared DML builders in pluginkit.
func (msDialect) QuoteIdent(name string) string {
	if q, err := sqltext.QuoteIdent("mssql", name); err == nil {
		return q
	}
	quoted := strings.ReplaceAll(name, "]", "]]")
	return "[" + quoted + "]"
}

// Placeholder renders SQL Server's named parameter (@p1, @p2, ...).
func (msDialect) Placeholder(i int) string { return fmt.Sprintf("@p%d", i) }

// Metrics returns curated read-only SQL Server metrics.
func (msDialect) Metrics(db *sql.DB) ([]proto.Metric, error) {
	const q = `
		SELECT 'Version', CAST(SERVERPROPERTY('ProductVersion') AS varchar(64))
		UNION ALL SELECT 'Edition', CAST(SERVERPROPERTY('Edition') AS varchar(64))
		UNION ALL SELECT 'Active connections', CAST(COUNT(*) AS varchar) FROM sys.dm_exec_sessions WHERE is_user_process = 1
		UNION ALL SELECT 'Databases', CAST(COUNT(*) AS varchar) FROM sys.databases`
	return pluginkit.KVMetrics(db, "SQL Server", q)
}

// CreateDatabase creates a new SQL Server database. CREATE DATABASE cannot run
// inside a user transaction, so it is issued on a dedicated connection.
func (msDialect) CreateDatabase(db *sql.DB, name, _, _ string) error {
	quoted := strings.ReplaceAll(name, "]", "]]")
	q := "CREATE DATABASE [" + quoted + "]"
	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("sqlserver create database: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), q); err != nil {
		return fmt.Errorf("sqlserver create database %q: %w", name, err)
	}
	return nil
}

func main() { pluginkit.Main(msDialect{}) }
