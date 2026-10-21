// Command oracledriver is the Oracle driver plugin. It is a standalone binary
// the app spawns and talks to over stdio (see internal/drivercore). It owns the
// go-ora driver and its *sql.DB, so the main app binary never imports it.
//
// Build:  go build -o plugins/oracle ./cmd/oracledriver   (or task build-plugins)
// Install: drop the binary in the state dir's plugins/ folder; the app finds it
// by the name "oracle" (".exe" on Windows).
package main

import (
	"database/sql"
	"os"

	go_ora "github.com/sijms/go-ora/v2"

	"tablepro-fyne/internal/drivercore"
)

type oracleDriver struct{}

// Open builds the go-ora connector. go-ora percent-encodes credentials inside
// BuildUrl, so a password with special characters survives intact.
func (oracleDriver) Open(cfg drivercore.ConnConfig) (*sql.DB, error) {
	conn := go_ora.NewConnector(go_ora.BuildUrl(
		cfg.Host, cfg.Port, cfg.Database, cfg.Username, cfg.Password, nil))
	return sql.OpenDB(conn), nil
}

func main() {
	drivercore.Serve(oracleDriver{}, os.Stdin, os.Stdout)
}
