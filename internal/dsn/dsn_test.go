package dsn

import (
	"strings"
	"testing"

	"tablepro-fyne/internal/drivercore"
)

// TestMssqlURLEscapesCredentials guards the generated sqlserver DSN: a valid
// encrypt= value, encryption by default, no trust of the server certificate
// unless asked, and percent-encoded credentials so special characters survive
// msdsn parsing.
func TestMssqlURLEscapesCredentials(t *testing.T) {
	cfg := drivercore.ConnConfig{
		Type: "sqlserver", Host: "h", Port: 1433,
		Username: "u@x", Password: "p/w:1", Database: "master", SSLMode: "",
	}
	got := MSSQL(cfg)
	for _, want := range []string{
		"sqlserver://u%40x:p%2Fw%3A1@h:1433",
		"encrypt=true",
		"TrustServerCertificate=false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("MSSQL missing %q, got %q", want, got)
		}
	}
	if got := MSSQL(drivercore.ConnConfig{Type: "sqlserver", Host: "h", SSLMode: "disable"}); !strings.Contains(got, "encrypt=disable") {
		t.Errorf("the off mode must really turn encryption off, got %q", got)
	}
	got = MSSQL(drivercore.ConnConfig{Type: "sqlserver", Host: "h", SSLMode: "trust"})
	if !strings.Contains(got, "encrypt=true") || !strings.Contains(got, "TrustServerCertificate=true") {
		t.Errorf("the trust mode must encrypt without verifying the chain, got %q", got)
	}
}

// TestPgURLEscapesCredentials: the postgres URL DSN must escape the userinfo as
// well; lib/pq unescapes it.
func TestPgURLEscapesCredentials(t *testing.T) {
	got := PG(drivercore.ConnConfig{
		Type: "postgresql", Host: "h", Port: 5432,
		Username: "u", Password: "p@ss w:1", Database: "db", SSLMode: "true",
	})
	want := "postgres://u:p%40ss%20w%3A1@h:5432/db?sslmode=require"
	if got != want {
		t.Errorf("PG = %q, want %q", got, want)
	}
}

// MSSQLEncrypt must always return a value go-mssqldb's msdsn parser accepts
// ("disable" case-insensitive, or a boolean). Empty / "require" previously
// produced "invalid encrypt ...: strconv.ParseBool ... invalid syntax".
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
		if got := MSSQLEncrypt(mode); got != want {
			t.Errorf("MSSQLEncrypt(%q) = %q, want %q", mode, got, want)
		}
	}
}

// TrustServerCertificate was hardcoded to true, which let TLS be stepped on by
// anyone with a certificate the client would accept. It must now default to
// verifying and only answer to the mode that says so by name.
func TestMssqlTrustServerCertificate(t *testing.T) {
	for _, mode := range []string{"", "true", "require", "verify-full", "disable", "false", "junk"} {
		if got := MSSQLTrustServerCertificate(mode); got != "false" {
			t.Errorf("MSSQLTrustServerCertificate(%q) = %q, want false", mode, got)
		}
	}
	if got := MSSQLTrustServerCertificate("trust"); got != "true" {
		t.Errorf("the named trust mode = %q, want true", got)
	}
}

// NormalizeSSL maps the GUI choices onto lib/pq sslmode values.
func TestNormalizeSSL(t *testing.T) {
	cases := map[string]string{
		"true":        "require",
		"false":       "disable",
		"":            "",
		"require":     "require",
		"verify-full": "verify-full",
		"prefer":      "prefer",
		"garbage":     "disable",
	}
	for mode, want := range cases {
		if got := NormalizeSSL(mode); got != want {
			t.Errorf("NormalizeSSL(%q) = %q, want %q", mode, got, want)
		}
	}
}
