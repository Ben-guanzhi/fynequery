package main

// The plugin main is a thin dialect adapter over dbcore and pluginkit; these
// tests pin its pure surface — identity, the browse statement shape, and the
// quoting/placeholder vocabulary the shared DML builders rely on — without a
// server. The connection path is covered by the contract tests against a real
// SQL Server (dbcore) and the pluginkit handler suite.

import "testing"

func TestMSSQLIdentity(t *testing.T) {
	d := msDialect{}
	if d.Code() != "mssql" || d.Name() != "SQL Server" || d.DefaultPort() != "1433" {
		t.Fatalf("identity = %q/%q/%q", d.Code(), d.Name(), d.DefaultPort())
	}
	if d.Version() == "" {
		t.Fatal("Version is empty")
	}
}

func TestMSSQLBrowseSQL(t *testing.T) {
	if got := (msDialect{}).BrowseSQL("orders", 50); got != "SELECT TOP (50) * FROM [orders]" {
		t.Fatalf("BrowseSQL = %q", got)
	}
	// An embedded bracket is doubled, the SQL Server escape.
	if got := (msDialect{}).BrowseSQL("we]ird", 1); got != "SELECT TOP (1) * FROM [we]]ird]" {
		t.Fatalf("BrowseSQL(esc) = %q", got)
	}
}

func TestMSSQLQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"orders": "[orders]",
		"we]ird": "[we]]ird]",
	}
	for in, want := range cases {
		if got := (msDialect{}).QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMSSQLPlaceholder(t *testing.T) {
	if got := (msDialect{}).Placeholder(2); got != "@p2" {
		t.Fatalf("Placeholder(2) = %q, want @p2", got)
	}
}
