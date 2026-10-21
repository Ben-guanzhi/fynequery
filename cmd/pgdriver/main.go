// Command pgdriver is the PostgreSQL driver plugin. It is a standalone binary
// the app spawns and talks to over stdio (see internal/drivercore). It owns the
// lib/pq driver and its *sql.DB, so the main app binary never imports lib/pq.
//
// Build:  go build -o plugins/postgresql ./cmd/pgdriver   (or task build-plugins)
// Install: drop the binary in the state dir's plugins/ folder; the app finds it
// by the name "postgresql" (".exe" on Windows).
package main

import (
	"database/sql"
	"os"

	_ "github.com/lib/pq"

	"tablepro-fyne/internal/drivercore"
	"tablepro-fyne/internal/dsn"
)

type pgDriver struct{}

// Open builds the lib/pq DSN and opens the pool. The DSN shape lives in
// internal/dsn so the app's tests assert the exact string this dials.
func (pgDriver) Open(cfg drivercore.ConnConfig) (*sql.DB, error) {
	return sql.Open("postgres", dsn.PG(cfg))
}

func main() {
	drivercore.Serve(pgDriver{}, os.Stdin, os.Stdout)
}
