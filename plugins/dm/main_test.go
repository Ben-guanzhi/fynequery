package main

// The plugin main is a thin dialect adapter over dbcore and pluginkit; these
// tests pin its pure surface — identity, the browse statement shape, and the
// quoting/placeholder vocabulary the shared DML builders rely on — without a
// server. DM's dictionary is exercised live by the kernel's dm suite
// (DBCORE_TEST_DM_DSN); the connection path is the pluginkit handler's.

import (
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/dm"
)

func TestDMIdentity(t *testing.T) {
	d := dmDialect{}
	if d.Code() != "dm" || d.Name() != "达梦 DM8" || d.DefaultPort() != "5236" {
		t.Fatalf("identity = %q/%q/%q", d.Code(), d.Name(), d.DefaultPort())
	}
	if d.Version() == "" {
		t.Fatal("Version is empty")
	}
}

func TestDMBrowseSQL(t *testing.T) {
	if got := (dmDialect{}).BrowseSQL("orders", 50); got != `SELECT * FROM "orders" WHERE ROWNUM <= 50` {
		t.Fatalf("BrowseSQL = %q", got)
	}
	// An embedded quote is doubled, the Oracle-compatible escape.
	if got := (dmDialect{}).BrowseSQL(`we"ird`, 1); got != `SELECT * FROM "we""ird" WHERE ROWNUM <= 1` {
		t.Fatalf("BrowseSQL(esc) = %q", got)
	}
}

func TestDMQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"orders": `"orders"`,
		`we"ird`: `"we""ird"`,
	}
	for in, want := range cases {
		if got := (dmDialect{}).QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDMPlaceholder(t *testing.T) {
	if got := (dmDialect{}).Placeholder(3); got != ":3" {
		t.Fatalf("Placeholder(3) = %q, want :3", got)
	}
}

func TestDMDsnTravelsDatabaseAsSchema(t *testing.T) {
	// The kernel's DSN builder maps "database" to the driver's schema
	// parameter — DM8 has no separate database layer inside a session.
	dsn := dm.DSN(dbcore.ConnSpec{Host: "localhost", Port: 5236, User: "SYSDBA", Password: "x", Database: "SYSDBA"})
	if !strings.HasPrefix(dsn, "dm://") || !strings.Contains(dsn, "schema=SYSDBA") {
		t.Fatalf("DSN = %q, want a dm:// URL carrying schema=SYSDBA", dsn)
	}
}
