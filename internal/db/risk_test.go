package db

import "testing"

// TestDestructiveRisk pins which statements stop and ask. The rule behind the
// list: a statement whose damage is not limited by the text in front of you -
// no WHERE, or the object itself is what disappears.
func TestDestructiveRisk(t *testing.T) {
	cases := []struct {
		name       string
		stmt       string
		wantKind   DestructiveKind
		wantReason bool
	}{
		{"drop table", "DROP TABLE users", RiskDrop, true},
		{"drop view", "drop view v", RiskDrop, true},
		{"drop index", "DROP INDEX i ON t", RiskDrop, true},
		{"truncate", "TRUNCATE TABLE users", RiskTruncate, true},
		{"truncate without keyword", "TRUNCATE users", RiskTruncate, true},
		{"alter drop column", "ALTER TABLE t DROP COLUMN c", RiskAlter, true},
		{"alter drop constraint", "ALTER TABLE t DROP CONSTRAINT k", RiskAlter, true},
		{"update every row", "UPDATE users SET active = 0", RiskUpdate, true},
		{"delete every row", "DELETE FROM users", RiskDelete, true},
		{"delete with a filter is fine", "DELETE FROM users WHERE id = 1", RiskNone, false},
		{"update with a filter is fine", "UPDATE users SET a = 1 WHERE id = 2", RiskNone, false},
		{"alter that only adds is fine", "ALTER TABLE t ADD COLUMN c INT", RiskNone, false},
		{"select is fine", "SELECT * FROM users", RiskNone, false},
		{"insert is fine", "INSERT INTO t (a) VALUES (1)", RiskNone, false},
		{"a commented-out delete is not a delete", "-- DELETE FROM users", RiskNone, false},
		{"a delete inside a literal is not a delete", "SELECT 'DELETE FROM users'", RiskNone, false},
		{"a drop inside a block comment is not a drop", "/* DROP TABLE t */ SELECT 1", RiskNone, false},
		{"a filter that only looks like one still counts as typed", "DELETE FROM t WHERE 1 = 1", RiskNone, false},
		{"whitespace only", "   ", RiskNone, false},
	}
	for _, tc := range cases {
		kind, reason := DestructiveRisk(tc.stmt)
		if kind != tc.wantKind {
			t.Errorf("%s: kind = %q, want %q", tc.name, kind, tc.wantKind)
		}
		if (reason != "") != tc.wantReason {
			t.Errorf("%s: reason = %q, want one = %v", tc.name, reason, tc.wantReason)
		}
	}
}

// TestDestructiveRiskIgnoresWordsInsideText: a keyword that only appears
// inside a literal or a comment must not set the guard off. A formatter that
// reads them as SQL is how a warning becomes a lie.
func TestDestructiveRiskIgnoresWordsInsideText(t *testing.T) {
	cases := []struct{ name, stmt string }{
		{"drop in a string", "SELECT 'drop table users' AS note"},
		{"truncate in a string", "SELECT 'truncate table users' AS note"},
		{"a where that is only data", "SELECT 'where' AS note"},
		{"the whole delete commented out", "-- delete from users\nSELECT 1"},
		{"a quoted identifier named delete", "SELECT `delete` FROM t"},
	}
	for _, tc := range cases {
		if kind, reason := DestructiveRisk(tc.stmt); kind != RiskNone || reason != "" {
			t.Errorf("%s: flagged %q as destructive", tc.name, tc.stmt)
		}
	}
}

// TestDestructiveRiskStaysQuietOnWhatItCannotJudge is the other half of the
// contract: refusing to guess is not the same as guessing "safe", and a
// statement the classifier does not understand must pass through unprompted.
func TestDestructiveRiskStaysQuietOnWhatItCannotJudge(t *testing.T) {
	cases := []string{
		"SELECT * FROM t",
		"SHOW TABLES",
		"BEGIN",
		"COMMIT",
		"CREATE TABLE t (id INT)",
		"CALL rebuild_stats()",
		"EXPLAIN SELECT 1",
	}
	for _, stmt := range cases {
		if kind, _ := DestructiveRisk(stmt); kind != RiskNone {
			t.Errorf("flagged %q as %q", stmt, kind)
		}
	}
}
