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
	rows, err := db.Query(`
		SELECT name, row_count, is_view FROM (
			SELECT t.name AS name,
			       CAST(SUM(p.rows) AS bigint) AS row_count,
			       0 AS is_view
			FROM sys.tables t
			JOIN sys.partitions p ON p.[object_id] = t.[object_id] AND p.index_id IN (0, 1)
			GROUP BY t.name
			UNION ALL
			SELECT v.name, CAST(-1 AS bigint), 1 FROM sys.views v
		) x
		ORDER BY is_view, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proto.ObjectInfo
	for rows.Next() {
		var name string
		var n interface{}
		var isView bool
		if err := rows.Scan(&name, &n, &isView); err != nil {
			return nil, err
		}
		obj := proto.ObjectInfo{Name: name, Rows: pluginkit.Atoi64(n), ApproxRows: true}
		if isView {
			obj.Kind, obj.Rows = "view", -1
		} else {
			obj.Kind = "table"
		}
		out = append(out, obj)
	}
	return out, rows.Err()
}

func (msDialect) ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error) {
	rows, err := db.Query(`
		SELECT c.name, tp.name, c.is_nullable
		FROM sys.columns c
		JOIN sys.types tp ON tp.user_type_id = c.user_type_id
		WHERE c.[object_id] = OBJECT_ID(@p1)
		ORDER BY c.column_id`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proto.ColumnInfo
	for rows.Next() {
		var c proto.ColumnInfo
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pks, err := scanStrings(db, `
		SELECT c.name
		FROM sys.indexes i
		JOIN sys.index_columns ic ON ic.[object_id] = i.[object_id] AND ic.index_id = i.index_id
		JOIN sys.columns c ON c.[object_id] = ic.[object_id] AND c.column_id = ic.column_id
		WHERE i.is_primary_key = 1 AND i.[object_id] = OBJECT_ID(@p1)`, table)
	if err != nil {
		return out, nil // PK info is best effort
	}
	for _, pk := range pks {
		for i := range out {
			if strings.EqualFold(out[i].Name, pk) {
				out[i].Key = "PK"
			}
		}
	}
	return out, nil
}

func (msDialect) BrowseSQL(table string, limit int) string {
	// [] is not standard SQL but is the SQL Server quote character.
	quoted := strings.ReplaceAll(table, "]", "]]")
	return fmt.Sprintf("SELECT TOP (%d) * FROM [%s]", limit, quoted)
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

func scanStrings(db *sql.DB, query string, args ...interface{}) ([]string, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func main() { pluginkit.Main(msDialect{}) }
