package sqlscan

import "testing"

// TestFirstKeyword pins the trivia-skipping first-keyword extraction: a
// leading comment or whitespace no longer hides the keyword (the Safe Mode
// gate used to read a comment-led SELECT as an unknown, i.e. write,
// statement), while the word cut stays identical to the transport
// classifier's (space/tab/newline/semicolon/opening paren).
func TestFirstKeyword(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"SELECT 1", "SELECT"},
		{"  select * from t", "SELECT"},
		{"-- note\nSELECT 1", "SELECT"},
		{"/* header */ SELECT 1", "SELECT"},
		{"  /* c1 */ -- c2\nSHOW TABLES", "SHOW"},
		{"/* unterminated SELECT 1", ""}, // comment swallows the rest
		{"SELECT(1)", "SELECT"},
		{"SELECT; ", "SELECT"},
		{"SELECTED t", "SELECTED"}, // no prefix false-positives
		{"", ""},
		{"   ", ""},
		{"WITH x AS (SELECT 1) SELECT * FROM x", "WITH"},
		{"(SELECT 1)", ""}, // paren-first: same verdict as the old cut
	}
	for _, tc := range cases {
		if got := FirstKeyword(tc.in); got != tc.want {
			t.Errorf("FirstKeyword(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestIsReadOnlyCommentLed pins the gate bug the convergence fixed: Safe
// Mode read a comment-led SELECT as a write and prompted (or demanded the
// password) for every commented read.
func TestIsReadOnlyCommentLed(t *testing.T) {
	for _, s := range []string{"-- note\nSELECT 1", "/* header */ SHOW TABLES"} {
		if !IsReadOnly(s) {
			t.Errorf("IsReadOnly(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"-- cleanup\nDROP TABLE t", "-- c\nUPDATE t SET a=1"} {
		if IsReadOnly(s) {
			t.Errorf("IsReadOnly(%q) = true, want false", s)
		}
	}
}

// TestStatementRanges pins the editor splitter's range semantics: quoted,
// line-comment and block-comment semicolons never split — a comment spans
// into the statement that follows it, so range 2 holds everything up to the
// next TOP-LEVEL semicolon; the trailing unterminated span is reported and
// the caller decides whether to run it.
func TestStatementRanges(t *testing.T) {
	script := "SELECT 1;\nSELECT 'a;b';\n-- c;\nSELECT 2 /* ; */\nUPDATE t SET a=';'"
	rs := StatementRanges(script)
	if len(rs) != 3 {
		t.Fatalf("ranges = %d, want 3: %+v", len(rs), rs)
	}
	get := func(i int) string { return script[rs[i].Start:rs[i].End] }
	if got := get(0); got != "SELECT 1;" {
		t.Fatalf("range 0 = %q", got)
	}
	if got := get(1); got != "\nSELECT 'a;b';" {
		t.Fatalf("range 1 must keep the quoted semicolon: %q", got)
	}
	if got := get(2); got != "\n-- c;\nSELECT 2 /* ; */\nUPDATE t SET a=';'" {
		t.Fatalf("range 2: comment/block/literal semicolons must not split: %q", got)
	}
}

// TestSplitDelimitedSmoke keeps a direct pin next to the splitter now that it
// lives here (the DELIMITER/restore-path cases remain in backup_test.go):
// comments become whitespace in the emitted statements, quoted and
// literal semicolons survive.
func TestSplitDelimitedSmoke(t *testing.T) {
	got := SplitDelimited("SELECT 1;\nSELECT 'a;b';\n-- c;\nSELECT 2 /* ; */\nUPDATE t SET a=';'")
	want := []string{"SELECT 1", "SELECT 'a;b'", "SELECT 2  \nUPDATE t SET a=';'"}
	if len(got) != len(want) {
		t.Fatalf("split = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("split[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
