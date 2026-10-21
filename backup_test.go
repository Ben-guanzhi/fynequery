package main

import (
	"strings"
	"testing"

	"fynequery/proto"
)

type fakeSession struct {
	ran []string
}

func (f *fakeSession) Server() string                             { return "fake" }
func (f *fakeSession) Tables() ([]proto.ObjectInfo, error)        { return nil, nil }
func (f *fakeSession) Columns(string) ([]proto.ColumnInfo, error) { return nil, nil }
func (f *fakeSession) Browse(string, int) (proto.QueryResult, error) {
	return proto.QueryResult{}, nil
}
func (f *fakeSession) Close() {}

func (f *fakeSession) Query(sql string, _ int) (proto.QueryResult, error) {
	f.ran = append(f.ran, sql)
	s := func(v string) *string { return &v }
	switch {
	case strings.HasPrefix(sql, "SHOW FULL TABLES"):
		return proto.QueryResult{Rows: [][]*string{{s("users"), s("BASE TABLE")}, {s("v"), s("VIEW")}}}, nil
	case strings.HasPrefix(sql, "SHOW CREATE TABLE"):
		return proto.QueryResult{Rows: [][]*string{{s("users"), s("CREATE TABLE `users` (`id` int, `name` varchar(20))")}}}, nil
	case strings.HasPrefix(sql, "SELECT * FROM"):
		return proto.QueryResult{
			Columns: []proto.ColumnInfo{{Name: "id", Type: "int"}, {Name: "name", Type: "varchar"}},
			Rows:    [][]*string{{s("1"), s("a'b")}, {s("2"), nil}},
		}, nil
	}
	return proto.QueryResult{}, nil
}

func TestDumpDatabase(t *testing.T) {
	sess := &fakeSession{}
	out, err := dumpDatabase(sess, "mysql", "shop", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"DROP TABLE IF EXISTS `users`;",
		"CREATE TABLE `users`",
		"INSERT INTO `users` (`id`, `name`) VALUES",
		"  ('1', 'a''b'),",
		"  ('2', NULL);",
		"SET FOREIGN_KEY_CHECKS=1;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dump missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "`v`") {
		t.Fatalf("view should be skipped:\n%s", out)
	}
}

func TestRunSQLScript(t *testing.T) {
	sess := &fakeSession{}
	n, err := runSQLScript(sess, "SET a=1;\nINSERT INTO t VALUES ('x;y');")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("executed %d, want 2", n)
	}
}

func TestParseConnURL(t *testing.T) {
	cases := []struct {
		in                             string
		ok                             bool
		driver, host, port, user, pass string
		dbname                         string
	}{
		{"mysql://root:secret@localhost:3306/shop", true, "mysql", "localhost", "3306", "root", "secret", "shop"},
		{"postgres://u@db.host/appdb", true, "postgres", "db.host", "", "u", "", "appdb"},
		{"mssql://sa:pw@host:1433/master", true, "mssql", "host", "1433", "sa", "pw", "master"},
		{"nonsense", false, "", "", "", "", "", ""},
		{"foo://host/db", false, "", "", "", "", "", ""},
	}
	for _, tc := range cases {
		c, ok := parseConnURL(tc.in)
		if ok != tc.ok {
			t.Fatalf("%q: ok=%v want %v", tc.in, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if c.Driver != tc.driver || c.Host != tc.host || c.Port != tc.port ||
			c.User != tc.user || c.Password != tc.pass || c.Database != tc.dbname {
			t.Fatalf("%q: got %+v", tc.in, c)
		}
	}
}

func TestSplitStatements(t *testing.T) {
	script := "SET x=1;\n-- a comment; not a statement\nINSERT INTO t VALUES ('a;b', 'c''d');\n`weird;col` = 1;\n"
	got := splitStatements(script)
	want := []string{
		"SET x=1",
		"INSERT INTO t VALUES ('a;b', 'c''d')",
		"`weird;col` = 1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d stmts: %#v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stmt %d: %q want %q", i, got[i], want[i])
		}
	}
}

func TestJoinMySQLValues(t *testing.T) {
	a, b := "a'b", `c\d`
	row := []*string{&a, nil, &b}
	got := joinMySQLValues(row)
	want := `'a''b', NULL, 'c\\d'`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBuildCondition(t *testing.T) {
	ws := &workspace{conn: Connection{Driver: "mysql"}}
	cases := []struct{ col, op, val, want string }{
		{"id", "=", "42", "`id` = 42"},
		{"name", "=", "Al", "`name` = 'Al'"},
		{"name", "LIKE", "A%", "`name` LIKE 'A%'"},
		{"del", "IS NULL", "", "`del` IS NULL"},
		{"id", "IN", "1,2,3", "`id` IN (1,2,3)"},
		{"id", "IN", "(1,2)", "`id` IN (1,2)"},
		{"c", "=", "", ""},
	}
	for _, tc := range cases {
		if got := ws.buildCondition(tc.col, tc.op, tc.val); got != tc.want {
			t.Fatalf("%s %s %q: got %q want %q", tc.col, tc.op, tc.val, got, tc.want)
		}
	}
	if got := ws.whereClause(); got != "" {
		t.Fatalf("empty filter: %q", got)
	}
	ws.filterSQL = "`id` = 1"
	if got := ws.whereClause(); got != " WHERE `id` = 1" {
		t.Fatalf("whereClause: %q", got)
	}
}
