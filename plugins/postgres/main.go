// fq-plugin-postgres: PostgreSQL support for fynequery, delivered as an
// external plugin process speaking the fynequery plugin protocol.
package main

import (
	"database/sql"
	"fmt"
	"net"
	"net/url"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver

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
	rows, err := db.Query(`
		SELECT c.relname,
		       CASE WHEN c.relkind = 'v' THEN 1 ELSE 0 END,
		       GREATEST(CAST(c.reltuples AS bigint), -1)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema()
		  AND c.relkind IN ('r', 'p', 'v')
		ORDER BY 2, c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proto.ObjectInfo
	for rows.Next() {
		var name string
		var isView bool
		var n interface{}
		if err := rows.Scan(&name, &isView, &n); err != nil {
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

func (pgDialect) ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error) {
	rows, err := db.Query(`
		SELECT c.column_name, c.data_type, c.is_nullable = 'YES'
		FROM information_schema.columns c
		WHERE c.table_schema = current_schema() AND c.table_name = $1
		ORDER BY c.ordinal_position`, table)
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
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON kcu.constraint_name = tc.constraint_name
		 AND kcu.table_schema = tc.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY'
		  AND tc.table_schema = current_schema() AND tc.table_name = $1`, table)
	if err != nil {
		return out, nil // PK info is best effort
	}
	for _, pk := range pks {
		for i := range out {
			if out[i].Name == pk {
				out[i].Key = "PK"
			}
		}
	}
	return out, nil
}

func (pgDialect) BrowseSQL(table string, limit int) string {
	return fmt.Sprintf("SELECT * FROM %s LIMIT %d", quotePG(table), limit)
}

func quotePG(name string) string {
	escaped := ""
	for _, r := range name {
		if r == '"' {
			escaped += `"`
		}
		escaped += string(r)
	}
	return `"` + escaped + `"`
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

func main() { pluginkit.Main(pgDialect{}) }
