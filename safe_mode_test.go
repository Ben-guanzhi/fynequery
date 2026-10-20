package main

// The editor is the one path in the app that can send a statement to the
// server with no confirmation at all: grid writes get a preview and a
// transaction, DROP from the navigator gets a confirm, and a hand-typed
// DELETE FROM t used to just run. These tests pin that gate.

import (
	"strings"
	"testing"
)

// TestADestructiveRunAsksBeforeItWrites: with the caret on an unfiltered
// DELETE, the run must stop at the dialog - no history entry, nothing sent.
func TestADestructiveRunAsksBeforeItWrites(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	c.tabs.entry.setTextSilently("DELETE FROM items")

	before := len(c.win.Canvas().Overlays().List())
	c.runCurrent()

	if ran := c.store.History(); len(ran) != 0 {
		t.Fatalf("the statement reached the server before it was confirmed: %v", ran)
	}
	if got := len(c.win.Canvas().Overlays().List()); got <= before {
		t.Fatal("no confirmation dialog appeared")
	}
}

// TestAnUnfilteredUpdateAsksToo covers the other half of the classic mistake -
// UPDATE without WHERE is as destructive as DELETE without WHERE, and just as
// easy to send by accident.
func TestAnUnfilteredUpdateAsksToo(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	c.tabs.entry.setTextSilently("UPDATE items SET name = 'x'")

	c.runCurrent()

	if ran := c.store.History(); len(ran) != 0 {
		t.Fatalf("the statement reached the server before it was confirmed: %v", ran)
	}
}

// TestAFilteredRunDoesNotAsk is the other side of the ledger. A confirmation
// that fires on ordinary statements is worse than none: it trains people to
// click through without reading. So the guard has to stay quiet here.
func TestAFilteredRunDoesNotAsk(t *testing.T) {
	cases := []string{
		"DELETE FROM items WHERE id = 1",
		"UPDATE items SET name = 'y' WHERE id = 1",
		"SELECT * FROM items",
	}
	for _, sql := range cases {
		c := newPagingClient(t, 3, false, "10")
		c.tabs.entry.setTextSilently(sql)
		before := len(c.win.Canvas().Overlays().List())

		c.runCurrent()

		if got := len(c.win.Canvas().Overlays().List()); got != before {
			t.Errorf("%q asked for confirmation", sql)
		}
		if ran := c.store.History(); len(ran) == 0 {
			t.Errorf("%q never reached the server", sql)
		}
	}
}

// TestDestructiveWarningNamesTheStatement: the dialog has to quote what it is
// asking about. "This is destructive, continue?" is not a question the user can
// answer from the screen.
func TestDestructiveWarningNamesTheStatement(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	cases := []struct {
		name string
		plan runPlan
		want string
	}{
		{"delete without a filter", runPlan{stmts: []string{"DELETE FROM items"}}, "DELETE FROM items"},
		{"update without a filter", runPlan{stmts: []string{"UPDATE items SET a = 1"}}, "UPDATE items SET a = 1"},
		{"drop", runPlan{stmts: []string{"DROP TABLE items"}}, "DROP TABLE items"},
		{"truncate", runPlan{stmts: []string{"TRUNCATE TABLE items"}}, "TRUNCATE TABLE items"},
		{"alter dropping a column", runPlan{stmts: []string{"ALTER TABLE items DROP COLUMN name"}},
			"ALTER TABLE items DROP COLUMN name"},
	}
	for _, tc := range cases {
		got := c.destructiveWarning(tc.plan)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: warning = %q, want it to quote %q", tc.name, got, tc.want)
		}
	}
}

// TestAScriptCountsEveryDangerousStatement: a script is one action, so the
// dialog has to say how much of it is dangerous rather than quoting one line
// and hoping.
func TestAScriptCountsEveryDangerousStatement(t *testing.T) {
	c := newPagingClient(t, 3, false, "10")
	c.tabs.entry.setTextSilently("DELETE FROM items;\nSELECT * FROM items;\nUPDATE items SET name = 'z'")
	plan, ok := c.planForScope(scopeAll)
	if !ok {
		t.Fatal("the script did not resolve to any statement")
	}
	got := c.destructiveWarning(plan)
	if !strings.Contains(got, "2 条") {
		t.Errorf("warning = %q, want it to count the two dangerous statements", got)
	}
}
