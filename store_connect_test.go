package main

import (
	"strings"
	"testing"
)

// mssqlEncrypt must always return a value go-mssqldb's msdsn parser accepts
// ("disable" case-insensitive, or a boolean). Empty / "require" previously
// produced "invalid encrypt ...: strconv.ParseBool ... invalid syntax".
func TestMssqlEncryptValues(t *testing.T) {
	valid := map[string]bool{
		"disable": true,
		"true":    true,
		"false":   true,
	}
	for _, mode := range []string{"", "false", "true", "disable", "require", "verify-ca", "verify-full", "strict", "junk"} {
		got := mssqlEncrypt(mode)
		if !valid[got] {
			t.Errorf("mssqlEncrypt(%q) = %q, not a go-mssqldb-valid value", mode, got)
		}
	}
}

// createConnection for SQL Server must succeed (DSN parses) for every GUI SSL
// mode, including the empty default that used to break with a ParseBool error.
func TestSqlServerDSNParses(t *testing.T) {
	for _, mode := range []string{"", "false", "true", "disable", "require"} {
		cfg := ConnConfig{
			Type:     dbTypeSQLServer,
			Host:     "127.0.0.1",
			Port:     1433,
			Username: "sa",
			Password: "secret",
			Database: "master",
			SSLMode:  mode,
		}
		db, err := createConnection(cfg)
		if err != nil {
			t.Fatalf("sslMode=%q: createConnection error: %v", mode, err)
		}
		if db == nil {
			t.Fatalf("sslMode=%q: createConnection returned nil db", mode)
		}
		db.Close()
	}
}

// Guard: the generated sqlserver DSN must carry a valid encrypt= value and
// percent-encode credentials so special characters survive msdsn parsing.
func TestMssqlURLEscapesCredentials(t *testing.T) {
	cfg := ConnConfig{
		Type: dbTypeSQLServer, Host: "h", Port: 1433,
		Username: "u@x", Password: "p/w:1", Database: "master", SSLMode: "",
	}
	dsn := mssqlURL(cfg)
	for _, want := range []string{
		"sqlserver://u%40x:p%2Fw%3A1@h:1433",
		"encrypt=disable",
		"TrustServerCertificate=true",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("mssqlURL missing %q, got %q", want, dsn)
		}
	}
	if db, err := createConnection(cfg); err != nil {
		t.Errorf("createConnection(sqlserver): %v", err)
	} else if db != nil {
		db.Close()
	}
}

// The postgres URL DSN must escape the userinfo as well; lib/pq unescapes it.
func TestPgURLEscapesCredentials(t *testing.T) {
	dsn := pgURL(ConnConfig{
		Type: dbTypePostgres, Host: "h", Port: 5432,
		Username: "u", Password: "p@ss w:1", Database: "db", SSLMode: "true",
	})
	want := "postgres://u:p%40ss%20w%3A1@h:5432/db?sslmode=require"
	if dsn != want {
		t.Errorf("pgURL = %q, want %q", dsn, want)
	}
}