// Package dsn builds connection strings for the URL-style engines. It holds no
// database/sql driver import, so both the app's tests and the out-of-process
// driver plugins (cmd/pgdriver, cmd/mssqldriver, ...) can share it. Keeping the
// DSN shapes in one place is what lets a plugin own its driver while the app
// still asserts the exact string it will dial.
package dsn

import (
	"fmt"
	"net/url"
	"strings"

	"tablepro-fyne/internal/drivercore"
)

// PG builds a percent-encoded URL DSN for lib/pq, which unescapes the userinfo
// itself, so passwords with spaces, '@', '/' etc. survive intact.
func PG(cfg drivercore.ConnConfig) string {
	u := url.URL{
		Scheme: "postgres",
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Path:   cfg.Database,
	}
	if cfg.Username != "" || cfg.Password != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}
	q := url.Values{"sslmode": {NormalizeSSL(cfg.SSLMode)}}
	u.RawQuery = q.Encode()
	return u.String()
}

// MSSQL builds the sqlserver:// URL. go-mssqldb's msdsn parser unescapes the
// userinfo, so url.UserPassword keeps special-character credentials whole.
func MSSQL(cfg drivercore.ConnConfig) string {
	u := url.URL{
		Scheme: "sqlserver",
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
	}
	if cfg.Username != "" || cfg.Password != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}
	q := url.Values{
		"database":               {cfg.Database},
		"encrypt":                {MSSQLEncrypt(cfg.SSLMode)},
		"TrustServerCertificate": {MSSQLTrustServerCertificate(cfg.SSLMode)},
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// MSSQLEncrypt maps the GUI 加密 choice onto a value go-mssqldb can parse. Its
// msdsn parser accepts only "disable" or a boolean - "require", "strict" and
// the empty string all fail ParseBool, which is what the dialer test guards
// against.
//
// Encryption is the default: an unset or unrecognised mode encrypts, and
// turning it off is a choice that has to be named ("false"/"disable").
func MSSQLEncrypt(mode string) string {
	switch strings.ToLower(mode) {
	case "false", "disable":
		return "disable"
	default: // "", "true", "trust", anything else
		return "true"
	}
}

// MSSQLTrustServerCertificate is the verification knob this driver really
// offers, and it defaults to verifying: only the mode named "trust" skips the
// certificate chain.
func MSSQLTrustServerCertificate(mode string) string {
	if strings.ToLower(mode) == "trust" {
		return "true"
	}
	return "false"
}

// NormalizeSSL maps GUI-friendly values onto PostgreSQL sslmode values.
func NormalizeSSL(mode string) string {
	switch mode {
	case "true":
		return "require"
	case "false":
		return "disable"
	case "", "require", "disable", "verify-ca", "verify-full", "prefer":
		return mode
	}
	return "disable"
}
