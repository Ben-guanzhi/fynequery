package main

import (
	"strings"
	"testing"
)

// The IN branch used to wrap the raw filter value in parentheses and paste it
// straight into the WHERE clause, so a value like `1) OR (1=1` spliced a new
// predicate into the statement. Each element is now rendered as a literal, and
// these cases pin the quoting (and that the injection shape no longer escapes).
// The list keeps the original no-space form (`(1,2,3)`) that the existing
// TestBuildCondition pins.

func TestInListLiteralQuoting(t *testing.T) {
	cases := []struct {
		driver, in, want string
	}{
		// Numbers stay bare; a bare list gains its own parens.
		{"mysql", "1,2,3", "(1,2,3)"},
		// An outer paren pair is accepted but the elements are still quoted.
		{"mysql", "(1,2)", "(1,2)"},
		// Text elements are single-quoted.
		{"mysql", "a,b", "('a','b')"},
		// Embedded quotes are doubled (MySQL backslash pass first).
		{"mysql", "it's", "('it''s')"},
		// Backslash is doubled for MySQL.
		{"mysql", "a\\b", "('a\\\\b')"},
		// The injection payload becomes a literal element.
		{"mysql", "1) OR (1=1", "('1) OR (1=1')"},
		// A parenthesized injection payload that is not a single list: the
		// outer parens stay (they are not a matching pair), and the whole
		// thing is quoted as one element, so no token escapes the literal.
		{"mysql", "(1) OR (1=1", "('(1) OR (1=1')"},
		// Empty / whitespace-only collapse to no list.
		{"mysql", "", ""},
		{"mysql", " , , ", ""},
		// A single empty paren pair.
		{"mysql", "()", ""},
		// Oracle/Postgres dialects take the same path (plain quoting).
		{"postgres", "a,b", "('a','b')"},
	}
	for _, tc := range cases {
		if got := inListLiteral(tc.driver, tc.in); got != tc.want {
			t.Errorf("inListLiteral(%q, %q) = %q, want %q", tc.driver, tc.in, got, tc.want)
		}
	}
}

func TestBuildConditionIN(t *testing.T) {
	ws := &workspace{conn: Connection{Driver: "mysql"}}
	cases := []struct{ col, op, val, want string }{
		{"id", "IN", "1,2,3", "`id` IN (1,2,3)"},
		{"id", "IN", "(1,2)", "`id` IN (1,2)"},
		{"name", "IN", "Al,Bo", "`name` IN ('Al','Bo')"},
		// Injection payload must be neutralized into a literal.
		{"id", "IN", "1) OR (1=1", "`id` IN ('1) OR (1=1')"},
		// Empty value renders no condition.
		{"id", "IN", "", ""},
	}
	for _, tc := range cases {
		if got := ws.buildCondition(tc.col, tc.op, tc.val); got != tc.want {
			t.Errorf("buildCondition(%q,%q,%q) = %q, want %q",
				tc.col, tc.op, tc.val, got, tc.want)
		}
	}
}

// TestBuildConditionInjectionStaysLiteral proves the injected fragment cannot
// close the literal or splice a new predicate: the whole payload stays inside
// one quoted element and the rendered condition is exactly that literal.
func TestBuildConditionInjectionStaysLiteral(t *testing.T) {
	ws := &workspace{conn: Connection{Driver: "mysql"}}
	got := ws.buildCondition("id", "IN", "1) OR (1=1")
	if got != "`id` IN ('1) OR (1=1')" {
		t.Fatalf("unexpected SQL: %q", got)
	}
	// The value must appear quoted exactly once; no bare OR/= token is emitted.
	if n := strings.Count(got, "'1) OR (1=1'"); n != 1 {
		t.Fatalf("payload not quoted exactly once: %q", got)
	}
	// A well-formed injected predicate must not appear outside a literal.
	if strings.HasSuffix(got, "OR (1=1") || strings.HasSuffix(got, "=1") {
		t.Fatalf("payload escaped the literal: %q", got)
	}
}
