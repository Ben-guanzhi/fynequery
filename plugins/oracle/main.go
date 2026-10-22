// fq-plugin-oracle: Oracle support for fynequery, delivered as an external
// plugin process speaking the fynequery plugin protocol. Uses the pure-Go
// go-ora driver, so no Oracle Instant Client installation is required.
package main

import (
	"database/sql"
	"fmt"
	"strconv"

	goora "github.com/sijms/go-ora/v2"

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
	rows, err := db.Query(`
		SELECT t.table_name, NVL(t.num_rows, -1)
		FROM user_tables t
		ORDER BY t.table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proto.ObjectInfo
	for rows.Next() {
		var name string
		var n interface{}
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out = append(out, proto.ObjectInfo{Name: name, Kind: "table",
			Rows: pluginkit.Atoi64(n), ApproxRows: true})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	views, err := db.Query(`SELECT view_name FROM user_views ORDER BY view_name`)
	if err != nil {
		return out, nil // views are best effort
	}
	defer views.Close()
	for views.Next() {
		var name string
		if err := views.Scan(&name); err != nil {
			return out, nil
		}
		out = append(out, proto.ObjectInfo{Name: name, Kind: "view", Rows: -1})
	}
	return out, views.Err()
}

func (oraDialect) ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error) {
	rows, err := db.Query(`
		SELECT column_name, data_type, nullable = 'Y'
		FROM user_tab_columns
		WHERE table_name = :1
		ORDER BY column_id`, table)
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
		SELECT cc.column_name
		FROM user_constraints c
		JOIN user_cons_columns cc ON cc.constraint_name = c.constraint_name
		WHERE c.constraint_type = 'P' AND c.table_name = :1`, table)
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

func (oraDialect) BrowseSQL(table string, limit int) string {
	return fmt.Sprintf("SELECT * FROM %s FETCH FIRST %d ROWS ONLY", quoteOra(table), limit)
}

// CreateDatabase is intentionally unsupported: Oracle databases are created via
// DBCA or DBA tooling, not from an ordinary client session. We report this
// clearly so the UI can surface a helpful message rather than a confusing
// driver failure.
func (oraDialect) CreateDatabase(db *sql.DB, name, charset, collation string) error {
	return fmt.Errorf("oracle does not support creating databases from a client session; use DBCA or your DBA tooling")
}

func quoteOra(name string) string {
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

func main() { pluginkit.Main(oraDialect{}) }
