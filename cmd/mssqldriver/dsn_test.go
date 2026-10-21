package main

import (
	"database/sql"
	"strings"
	"testing"

	"tablepro-fyne/internal/drivercore"
	"tablepro-fyne/internal/dsn"
)

// open builds the DSN this plugin dials and opens the pool, so the tests below
// exercise the exact path Serve's Open takes.
func open(cfg drivercore.ConnConfig) (*sql.DB, error) {
	return sql.Open("sqlserver", dsn.MSSQL(cfg))
}

// TestMssqlDSNReachesTheDialer is the guard the parser comment claims: sql.Open
// defers parsing, so dial a port nothing listens on and require the failure to
// be the TCP refusal. An "invalid encrypt" answer means we produced a value
// go-mssqldb rejects, which is how the old mapping surfaced at connect time.
func TestMssqlDSNReachesTheDialer(t *testing.T) {
	for _, mode := range []string{"", "true", "trust", "false", "disable", "require", "strict"} {
		cfg := drivercore.ConnConfig{
			Type: "sqlserver", Host: "127.0.0.1", Port: 14333,
			Username: "sa", Password: "pw", Database: "master", SSLMode: mode,
		}
		db, err := open(cfg)
		if err != nil {
			t.Fatalf("sslMode=%q: %v", mode, err)
		}
		err = db.Ping()
		db.Close()
		if err == nil {
			t.Fatalf("sslMode=%q: expected the dial to fail, port 14333 is closed", mode)
		}
		if strings.Contains(err.Error(), "invalid encrypt") || strings.Contains(err.Error(), "ParseBool") {
			t.Errorf("sslMode=%q: driver rejected our encrypt value: %v", mode, err)
		}
		if !strings.Contains(err.Error(), "tcp") {
			t.Errorf("sslMode=%q: expected a TCP refusal, got %v", mode, err)
		}
	}
}

// TestSqlServerDSNParses: the DSN must parse (sql.Open succeeds) for every GUI
// SSL mode, including the empty default that used to break with a ParseBool
// error.
func TestSqlServerDSNParses(t *testing.T) {
	for _, mode := range []string{"", "false", "true", "disable", "require"} {
		cfg := drivercore.ConnConfig{
			Type:     "sqlserver",
			Host:     "127.0.0.1",
			Port:     1433,
			Username: "sa",
			Password: "secret",
			Database: "master",
			SSLMode:  mode,
		}
		db, err := open(cfg)
		if err != nil {
			t.Fatalf("sslMode=%q: open error: %v", mode, err)
		}
		if db == nil {
			t.Fatalf("sslMode=%q: open returned nil db", mode)
		}
		db.Close()
	}
}
