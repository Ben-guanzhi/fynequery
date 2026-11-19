// fq-plugin-dm: 达梦 DM8 support for fynequery, delivered as an external
// plugin process speaking the fynequery plugin protocol. Uses the pure-Go
// dameng driver (github.com/sophon-zt/sql-driver/dameng) and reuses the
// kernel's DM catalog, which rides the Oracle-compatible dictionary views.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/dm"
	"github.com/Ben-guanzhi/dbcore/sqltext"

	"fynequery/plugins/internal/pluginkit"
	"fynequery/proto"
)

const pluginVersion = "1.0.0"

type dmDialect struct{}

func (dmDialect) Code() string        { return "dm" }
func (dmDialect) Name() string        { return "达梦 DM8" }
func (dmDialect) Version() string     { return pluginVersion }
func (dmDialect) DefaultPort() string { return "5236" }

func (d dmDialect) Open(p proto.ConnectParams) (*sql.DB, string, error) {
	port := p.Port
	if port == "" {
		port = d.DefaultPort()
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, "", fmt.Errorf("invalid port %q", port)
	}
	// The kernel's DSN builder owns the dm:// shape (the "database" field
	// travels as the driver's schema parameter; defaults live there).
	dsn := dm.DSN(dbcore.ConnSpec{
		Host: p.Host, Port: portNum, User: p.User, Password: p.Password, Database: p.Database,
	})
	handle, server, err := pluginkit.OpenVerify("dm", dsn,
		`SELECT banner FROM v$version WHERE ROWNUM = 1`)
	if err != nil {
		return nil, "", fmt.Errorf("dm %s:%s/%s: %w", p.Host, port, p.Database, err)
	}
	if server != "" {
		server = "达梦 DM8 " + server
	}
	return handle, server, nil
}

func (dmDialect) ListTables(db *sql.DB) ([]proto.ObjectInfo, error) {
	// An empty schema makes the kernel reader use the session's current schema;
	// DM's dictionary is Oracle-compatible, so oracleRecipe holds.
	found, err := dm.NewFromDB(db).ListTables(context.Background(), "")
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

func (dmDialect) ListColumns(db *sql.DB, table string) ([]proto.ColumnInfo, error) {
	found, err := dm.NewFromDB(db).ListColumns(context.Background(), "", table)
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

func (dmDialect) BrowseSQL(table string, limit int) string {
	// DM rides Oracle's quoting in compatibility mode.
	quoted := strings.ReplaceAll(table, `"`, `""`)
	return fmt.Sprintf("SELECT * FROM \"%s\" WHERE ROWNUM <= %d", quoted, limit)
}

func (dmDialect) QuoteIdent(name string) string {
	if q, err := sqltext.QuoteIdent("dm", name); err == nil {
		return q
	}
	quoted := strings.ReplaceAll(name, `"`, `""`)
	return `"` + quoted + `"`
}

func (dmDialect) Placeholder(i int) string { return fmt.Sprintf(":%d", i) }

func (dmDialect) Metrics(db *sql.DB) ([]proto.Metric, error) {
	const q = `
		SELECT 'Version', banner FROM v$version WHERE ROWNUM = 1
		UNION ALL SELECT 'Sessions', CAST(COUNT(*) AS varchar) FROM v$session
		UNION ALL SELECT 'Users', CAST(COUNT(*) AS varchar) FROM all_users`
	return pluginkit.KVMetrics(db, "达梦 DM8", q)
}

func (dmDialect) CreateDatabase(db *sql.DB, name, _, _ string) error {
	quoted := strings.ReplaceAll(name, `"`, `""`)
	q := `CREATE TABLESPACE "` + quoted + `" DATAFILE '` + quoted + `.dbf' SIZE 128 AUTOEXTEND ON`
	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("dm create database: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), q); err != nil {
		return fmt.Errorf("dm create tablespace %q: %w", name, err)
	}
	return nil
}

func main() { pluginkit.Main(dmDialect{}) }
