package main

import (
	"strings"
	"testing"
)

// TestFuzzyScore checks the subsequence matcher: ordered presence is required,
// leading/contiguous matches score higher, and very long haystacks are
// penalised so the tightest name wins (TablePlus OpenAnything ranking).
func TestFuzzyScore(t *testing.T) {
	if _, ok := fuzzyScore("", "anything"); !ok {
		t.Fatal("empty needle must match")
	}
	if _, ok := fuzzyScore("xyz", "abc"); ok {
		t.Fatal("disjoint needle must not match")
	}
	// Shorter / tighter match outranks a spread-out one.
	sTight, _ := fuzzyScore("li", "lima")
	sLoose, _ := fuzzyScore("li", "aliiiibbb")
	if sTight <= sLoose {
		t.Fatalf("tighter match should score higher: tight=%d loose=%d", sTight, sLoose)
	}
	// A leading match outranks the same runes mid-word.
	sLead, _ := fuzzyScore("us", "users")
	sMid, _ := fuzzyScore("us", "abusers")
	if sLead <= sMid {
		t.Fatalf("leading match should score higher: lead=%d mid=%d", sLead, sMid)
	}
}

// TestQuickOpenSearch checks the palette returns case-insensitive hits from
// the history, returns everything for an empty pattern, and nothing for a
// pattern matching no candidate.
func TestQuickOpenSearch(t *testing.T) {
	ws := &workspace{side: newSidebar(func(string) {}, func() {})}
	ws.side.appendHistory("SELECT * FROM users")
	ws.side.appendHistory("SELECT name FROM customers")
	ws.side.appendHistory("UPDATE users SET active = 1")

	got := ws.quickOpenSearch("users")
	if len(got) == 0 {
		t.Fatal("expected at least one 'users' match")
	}
	// Fuzzy subsequence matching can also surface weaker hits (e.g. the
	// "u…s…e…r…s" inside "customers"), so we only require that at least one
	// returned hit literally contains "users" — the strongest one.
	found := false
	for _, m := range got {
		if strings.Contains(strings.ToLower(m.SQL), "users") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no returned match literally contains 'users': %+v", got)
	}

	// Empty pattern is unfiltered: every candidate comes back.
	all := ws.quickOpenSearch("")
	if len(all) < 3 {
		t.Fatalf("empty pattern returned %d, want all 3 history items", len(all))
	}

	// A pattern matching nothing returns empty.
	if got := ws.quickOpenSearch("qqqzzz"); len(got) != 0 {
		t.Fatalf("expected no matches, got %d", len(got))
	}
}

// TestQuickOpenCandidatesCaseInsensitive pins that table/search matches ignore
// case: a term in a different case still finds the candidate.
func TestQuickOpenCandidatesCaseInsensitive(t *testing.T) {
	ws := &workspace{side: newSidebar(func(string) {}, func() {})}
	ws.side.appendHistory("SELECT * FROM Users")
	got := ws.quickOpenSearch("USERS")
	if len(got) != 1 {
		t.Fatalf("case-insensitive search returned %d hits, want 1", len(got))
	}
}
