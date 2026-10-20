package db

import "testing"

// TestFormatSQL lays out the clauses it knows. The point of the exercise is
// that a script that grew over an afternoon - keywords in whatever case they
// were typed, clauses wherever the caret happened to be - comes back in one
// shape without changing what it does.
func TestFormatSQL(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"one clause per line", "select * from t where a=1",
			"SELECT *\nFROM t\nWHERE a = 1"},
		{"join and the rest", "SELECT id, name FROM users u LEFT JOIN orders o ON o.user_id = u.id " +
			"WHERE u.age >= 18 AND u.name LIKE 'a;b%' ORDER BY u.name DESC LIMIT 10",
			"SELECT id, name\nFROM users u\nLEFT JOIN orders o\nON o.user_id = u.id\n" +
				"WHERE u.age >= 18\nAND u.name LIKE 'a;b%'\nORDER BY u.name DESC\nLIMIT 10"},
		{"group by is one phrase", "SELECT COUNT(*) c, SUM(x) FROM t GROUP BY y HAVING SUM(x) > 1",
			"SELECT COUNT(*) c, SUM(x)\nFROM t\nGROUP BY y\nHAVING SUM(x) > 1"},
		{"insert head", "insert into t (a,b) values (1,'x'),(2,'y')",
			"INSERT INTO t (a, b)\nVALUES (1, 'x'), (2, 'y')"},
		{"update head", "update t set a = 1 where b = 2",
			"UPDATE t\nSET a = 1\nWHERE b = 2"},
		{"delete head", "delete from t where id = 1",
			"DELETE FROM t\nWHERE id = 1"},
		{"a subquery is a block", "SELECT * FROM (SELECT 1) x",
			"SELECT *\nFROM (\n  SELECT 1\n) x"},
		{"empty input stays empty", "   ", ""},
	}
	for _, tc := range cases {
		if got := FormatSQL(tc.in); got != tc.want {
			t.Errorf("%s: FormatSQL(%q) =\n%q\nwant\n%q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestFormatSQLLeavesTextAlone is the safety property of the whole file: the
// only bytes the formatter writes to a literal are the ones it read. A keyword
// inside a string is data, a comment is where the unfinished parts live, and a
// quoted identifier is how you say "this is a name, whatever it looks like".
func TestFormatSQLLeavesTextAlone(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"double spaces inside a literal survive", "SELECT 'a  b' FROM t",
			"SELECT 'a  b'\nFROM t"},
		{"a semicolon in a literal splits nothing", "SELECT ';' FROM t",
			"SELECT ';'\nFROM t"},
		{"lower case in a literal stays lower", "SELECT 'select from where' FROM t",
			"SELECT 'select from where'\nFROM t"},
		{"a doubled quote is one quote", "SELECT 'It''s' FROM t",
			"SELECT 'It''s'\nFROM t"},
		{"a backslash is not an escape here", `SELECT 'a\nb' FROM t`,
			"SELECT 'a\\nb'\nFROM t"},
		{"a line comment survives verbatim", "SELECT 1 -- keep  me\nFROM t",
			"SELECT 1 -- keep  me\nFROM t"},
		{"a block comment survives verbatim", "SELECT 1 /* keep\n me */ FROM t",
			"SELECT 1 /* keep\n me */\nFROM t"},
		{"a quoted identifier is never rewritten", "SELECT `from`, \"select\" FROM t",
			"SELECT `from`, \"select\"\nFROM t"},
		{"a word named like a clause stays as typed after SELECT", "SELECT select AS x FROM t",
			"SELECT select AS x\nFROM t"},
		{"a CJK identifier survives", "SELECT 名称 FROM 表",
			"SELECT 名称\nFROM 表"},
	}
	for _, tc := range cases {
		if got := FormatSQL(tc.in); got != tc.want {
			t.Errorf("%s: FormatSQL(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestFormatSQLIsIdempotent is what makes a keystroke-bound formatter safe to
// press twice: formatting its own output must be a no-op. It also happens to be
// the cheapest defence against a layout rule that drifts with each pass - the
// classic case is indentation that doubles every time you run it.
func TestFormatSQLIsIdempotent(t *testing.T) {
	inputs := []string{
		"select * from t where a=1",
		"SELECT id, name FROM users u LEFT JOIN orders o ON o.user_id = u.id " +
			"WHERE u.age >= 18 AND u.name LIKE 'a;b%' ORDER BY u.name DESC LIMIT 10",
		"insert into t (a,b) values (1,'x'),(2,'y')",
		"update t set a = 1 where b = 2",
		"delete from t where id = 1",
		"SELECT * FROM (SELECT 1) x WHERE EXISTS (SELECT * FROM b WHERE b.id = x.id)",
		"-- only a comment",
		"SELECT 1;\nSELECT 2;",
		"with c as (select 1 as x) select * from c",
		"select 名称 from 表 where 名称 like 'a%'",
	}
	for _, in := range inputs {
		first := FormatSQL(in)
		second := FormatSQL(first)
		if first != second {
			t.Errorf("not idempotent for %q:\nfirst:\n%s\nsecond:\n%s", in, first, second)
		}
	}
}

// TestFormatSQLKeepsScriptsSeparate: a script's statements keep their own
// semicolons and their own lines, so re-running it still means the same thing.
func TestFormatSQLKeepsScriptsSeparate(t *testing.T) {
	const in = "INSERT INTO t (a) VALUES (1);\nUPDATE t SET a = 2 WHERE a = 1;\nDELETE FROM t WHERE a = 2;"
	const want = "INSERT INTO t (a)\nVALUES (1);\nUPDATE t\nSET a = 2\nWHERE a = 1;\nDELETE FROM t\nWHERE a = 2;"
	if got := FormatSQL(in); got != want {
		t.Errorf("FormatSQL(script) =\n%s\nwant\n%s", got, want)
	}
}

// TestFormatSQLIsSafeOnHalfWrittenText: the formatter runs on what is in the
// editor, which is often an unfinished sentence. It must not invent a
// terminator or eat what is there.
func TestFormatSQLIsSafeOnHalfWrittenText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"an open string is left open", "SELECT 'abc", "SELECT 'abc"},
		{"an open comment is left open", "SELECT /* oops", "SELECT /* oops"},
		{"a dangling operator", "SELECT * FROM t WHERE", "SELECT *\nFROM t\nWHERE"},
	}
	for _, tc := range cases {
		if got := FormatSQL(tc.in); got != tc.want {
			t.Errorf("%s: changed %q into %q", tc.name, tc.in, got)
		}
	}
}
