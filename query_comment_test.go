package main

import "testing"

func TestToggleLineComment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "-- SELECT 1"},
		{"-- SELECT 1", "SELECT 1"},
		{"  SELECT 1", "  -- SELECT 1"}, // preserve indentation
		{"  -- SELECT 1", "  SELECT 1"}, // uncomment keeps indentation
		{"--SELECT 1", "SELECT 1"},      // "--" with no space still uncomments
		{"\tSELECT 1", "\t-- SELECT 1"}, // tab indent
	}
	for _, c := range cases {
		if got := toggleLineComment(c.in); got != c.want {
			t.Errorf("toggleLineComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The spaced form round-trips: comment then uncomment is a no-op.
	base := "    SELECT * FROM t"
	if got := toggleLineComment(toggleLineComment(base)); got != base {
		t.Errorf("round-trip: got %q, want %q", got, base)
	}
}
