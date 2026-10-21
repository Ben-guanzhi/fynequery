// Command mssqldriver is the SQL Server driver plugin. It is a standalone binary
// the app spawns and talks to over stdio (see internal/drivercore). It owns the
// go-mssqldb driver and its *sql.DB, so the main app binary never imports it.
//
// Build:  go build -o plugins/sqlserver ./cmd/mssqldriver   (or task build-plugins)
// Install: drop the binary in the state dir's plugins/ folder; the app finds it
// by the name "sqlserver" (".exe" on Windows).
package main

import (
	"database/sql"
	"os"

	_ "github.com/denisenkom/go-mssqldb"

	"tablepro-fyne/internal/drivercore"
	"tablepro-fyne/internal/dsn"
)

type mssqlDriver struct{}

// Open builds the sqlserver:// DSN and opens the pool. The DSN shape lives in
// internal/dsn so the app's tests assert the exact string this dials.
func (mssqlDriver) Open(cfg drivercore.ConnConfig) (*sql.DB, error) {
	return sql.Open("sqlserver", dsn.MSSQL(cfg))
}

func main() {
	drivercore.Serve(mssqlDriver{}, os.Stdin, os.Stdout)
}
