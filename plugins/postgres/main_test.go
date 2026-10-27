package main

// The plugin main is a thin dialect adapter over dbcore and pluginkit; these
// tests pin its pure surface — identity, the browse statement shape, and the
// quoting/placeholder vocabulary the shared DML builders rely on — without a
// server. The connection path is covered by the contract tests against a real
// PostgreSQL (dbcore) and the pluginkit handler suite.

import "testing"

func TestPostgresIdentity(t *testing.T) {
	d := pgDialect{}
	if d.Code() != "postgres" || d.Name() != "PostgreSQL" || d.DefaultPort() != "5432" {
		t.Fatalf("identity = %q/%q/%q", d.Code(), d.Name(), d.DefaultPort())
	}
	if d.Version() == "" {
		t.Fatal("Version is empty")
	}
}

func TestPostgresBrowseSQL(t *testing.T) {
	got := pgDialect{}.BrowseSQL(`orders`, 50)
	if got != `SELECT * FROM "orders" LIMIT 50` {
		t.Fatalf("BrowseSQL = %q", got)
	}
}

func TestPostgresQuoteIdent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"orders", `"orders"`},
		{`we"ird`, `"we""ird"`},
		{"CamelCase", `"CamelCase"`},
	}
	for _, c := range cases {
		if got := quotePG(c.in); got != c.want {
			t.Errorf("quotePG(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPostgresPlaceholder(t *testing.T) {
	if got := (pgDialect{}).Placeholder(2); got != "$2" {
		t.Fatalf("Placeholder(2) = %q, want $2", got)
	}
}
