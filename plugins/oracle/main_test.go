package main

// The plugin main is a thin dialect adapter over dbcore and pluginkit; these
// tests pin its pure surface — identity, the browse statement shape, and the
// quoting/placeholder vocabulary the shared DML builders rely on — without a
// server. The connection path is covered by the contract tests against a real
// Oracle (dbcore) and the pluginkit handler suite.

import (
	"strings"
	"testing"
)

func TestOracleIdentity(t *testing.T) {
	d := oraDialect{}
	if d.Code() != "oracle" || d.Name() != "Oracle" || d.DefaultPort() != "1521" {
		t.Fatalf("identity = %q/%q/%q", d.Code(), d.Name(), d.DefaultPort())
	}
	if d.Version() == "" {
		t.Fatal("Version is empty")
	}
}

func TestOracleBrowseSQL(t *testing.T) {
	if got := (oraDialect{}).BrowseSQL("orders", 50); got != `SELECT * FROM "orders" FETCH FIRST 50 ROWS ONLY` {
		t.Fatalf("BrowseSQL = %q", got)
	}
}

func TestOracleQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"orders": `"orders"`,
		`we"ird`: `"we""ird"`,
	}
	for in, want := range cases {
		if got := quoteOra(in); got != want {
			t.Errorf("quoteOra(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOraclePlaceholder(t *testing.T) {
	if got := (oraDialect{}).Placeholder(2); got != ":2" {
		t.Fatalf("Placeholder(2) = %q, want :2", got)
	}
}

func TestOracleCreateDatabaseRefuses(t *testing.T) {
	// Oracle databases are not creatable from an ordinary session; the dialect
	// must say so clearly rather than return a driver error.
	err := oraDialect{}.CreateDatabase(nil, "newdb", "", "")
	if err == nil || !strings.Contains(err.Error(), "DBCA") {
		t.Fatalf("CreateDatabase = %v, want the DBCA guidance", err)
	}
}
