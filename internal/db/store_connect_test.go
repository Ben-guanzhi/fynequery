package db

import (
	"context"
	"strings"
	"testing"
	"time"
)

// mssqlEncrypt must always return a value go-mssqldb's msdsn parser accepts
// ("disable" case-insensitive, or a boolean). Empty / "require" previously
// produced "invalid encrypt ...: strconv.ParseBool ... invalid syntax".
// Encryption is the default, so only the two names for "off" turn it off.
func TestMssqlEncryptValues(t *testing.T) {
	cases := map[string]string{
		"":            "true", // unset now means encrypted
		"true":        "true",
		"trust":       "true",
		"require":     "true",
		"verify-ca":   "true",
		"verify-full": "true",
		"strict":      "true", // not a value this driver parses, so it must map
		"junk":        "true",
		"false":       "disable",
		"disable":     "disable",
		"DISABLE":     "disable",
	}
	for mode, want := range cases {
		if got := mssqlEncrypt(mode); got != want {
			t.Errorf("mssqlEncrypt(%q) = %q, want %q", mode, got, want)
		}
	}
}

// TrustServerCertificate was hardcoded to true, which let TLS be stepped on by
// anyone with a certificate the client would accept. It must now default to
// verifying and only answer to the mode that says so by name.
func TestMssqlTrustServerCertificate(t *testing.T) {
	for _, mode := range []string{"", "true", "require", "verify-full", "disable", "false", "junk"} {
		if got := mssqlTrustServerCertificate(mode); got != "false" {
			t.Errorf("mssqlTrustServerCertificate(%q) = %q, want false", mode, got)
		}
	}
	if got := mssqlTrustServerCertificate("trust"); got != "true" {
		t.Errorf("the named trust mode = %q, want true", got)
	}
}

// TestMssqlDSNReachesTheDialer is the guard the parser comment claims: sql.Open
// defers parsing, so dial a port nothing listens on and require the failure to
// be the TCP refusal. An "invalid encrypt" answer means we produced a value
// go-mssqldb rejects, which is how the old mapping surfaced at connect time.
func TestMssqlDSNReachesTheDialer(t *testing.T) {
	for _, mode := range []string{"", "true", "trust", "false", "disable", "require", "strict"} {
		cfg := ConnConfig{
			Type: DbTypeSQLServer, Host: "127.0.0.1", Port: 14333,
			Username: "sa", Password: "pw", Database: "master", SSLMode: mode,
		}
		db, err := createConnection(cfg)
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

// createConnection for SQL Server must succeed (DSN parses) for every GUI SSL
// mode, including the empty default that used to break with a ParseBool error.
func TestSqlServerDSNParses(t *testing.T) {
	for _, mode := range []string{"", "false", "true", "disable", "require"} {
		cfg := ConnConfig{
			Type:     DbTypeSQLServer,
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

// Guard: the generated sqlserver DSN must carry a valid encrypt= value, must
// encrypt by default, must not trust the server certificate unless asked, and
// must percent-encode credentials so special characters survive msdsn parsing.
func TestMssqlURLEscapesCredentials(t *testing.T) {
	cfg := ConnConfig{
		Type: DbTypeSQLServer, Host: "h", Port: 1433,
		Username: "u@x", Password: "p/w:1", Database: "master", SSLMode: "",
	}
	dsn := mssqlURL(cfg)
	for _, want := range []string{
		"sqlserver://u%40x:p%2Fw%3A1@h:1433",
		"encrypt=true",
		"TrustServerCertificate=false",
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

	// The two named departures from the default.
	if got := mssqlURL(ConnConfig{Type: DbTypeSQLServer, Host: "h", SSLMode: "disable"}); !strings.Contains(got, "encrypt=disable") {
		t.Errorf("the off mode must really turn encryption off, got %q", got)
	}
	got := mssqlURL(ConnConfig{Type: DbTypeSQLServer, Host: "h", SSLMode: "trust"})
	if !strings.Contains(got, "encrypt=true") || !strings.Contains(got, "TrustServerCertificate=true") {
		t.Errorf("the trust mode must encrypt without verifying the chain, got %q", got)
	}
}

// The postgres URL DSN must escape the userinfo as well; lib/pq unescapes it.
func TestPgURLEscapesCredentials(t *testing.T) {
	dsn := pgURL(ConnConfig{
		Type: DbTypePostgres, Host: "h", Port: 5432,
		Username: "u", Password: "p@ss w:1", Database: "db", SSLMode: "true",
	})
	want := "postgres://u:p%40ss%20w%3A1@h:5432/db?sslmode=require"
	if dsn != want {
		t.Errorf("pgURL = %q, want %q", dsn, want)
	}
}

// TestTestConnectionHonorCancel pins the whole point of ctx: against a host
// that never answers (a non-routable address, so the dial just sits there) a
// canceled context must make TestConnection give up quickly instead of
// hanging the 停止 button until the OS times out the dial.
func TestTestConnectionHonorCancel(t *testing.T) {
	// 10.255.255.1 is RFC1918 and black-holed on ordinary networks: a TCP
	// connect to it neither succeeds nor gets a fast RST, so without
	// cancellation the handshake would outlive any reasonable wait.
	cfg := ConnConfig{
		Type:     DbTypeMySQL,
		Host:     "10.255.255.1",
		Port:     3306,
		Username: "u",
		Password: "p",
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	db, err := TestConnection(ctx, cfg)
	if db != nil {
		db.Close()
	}
	if err == nil {
		t.Fatal("a canceled handshake against a dead host answered success")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("cancellation did not bound the handshake: took %s", elapsed)
	}
}
