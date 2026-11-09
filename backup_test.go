package main

import (
	"strings"
	"testing"

	"github.com/dbcore/dbcore/sqltext"

	"fynequery/proto"
)

type fakeSession struct {
	ran      []string
	streamed []string
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
	s := func(v string) proto.CellPayload { return proto.CellPayload{Kind: "text", Text: v} }
	switch {
	case strings.HasPrefix(sql, "SHOW FULL TABLES"):
		return proto.QueryResult{Rows: [][]proto.CellPayload{{s("users"), s("BASE TABLE")}, {s("v"), s("VIEW")}}}, nil
	case strings.HasPrefix(sql, "SHOW CREATE TABLE"):
		return proto.QueryResult{Rows: [][]proto.CellPayload{{s("users"), s("CREATE TABLE `users` (`id` int, `name` varchar(20))")}}}, nil
	}
	return proto.QueryResult{}, nil
}

// streamRows 是 fake 的流式数据，镜像真实契约：StreamRunCtx 的首次 flush 把
// 列元数据和首批行放在同一帧发出（"cols 单独成帧"从不存在）。回归守卫：
// dump 回调曾把 cols 帧当纯元数据丢弃，每张表的前 dumpBatch 行静默消失。
func (f *fakeSession) QueryStream(sql string, batch int, emit func(cols []proto.ColumnInfo, rows [][]*string) (bool, error)) (proto.QueryResult, error) {
	f.streamed = append(f.streamed, sql)
	if !strings.HasPrefix(sql, "SELECT * FROM") {
		return proto.QueryResult{}, nil
	}
	s := func(v string) *string { return &v }
	// 两行都装进首批（batch≥2）：整表恰好一帧。
	keep, err := emit([]proto.ColumnInfo{{Name: "id"}, {Name: "name"}},
		[][]*string{{s("1"), s("a'b")}, {s("2"), nil}})
	if err != nil || !keep {
		return proto.QueryResult{}, err
	}
	return proto.QueryResult{}, nil
}

func TestDumpDatabase(t *testing.T) {
	sess := &fakeSession{}
	var out strings.Builder
	n, warnings, err := dumpDatabase(sess, "mysql", "shop", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(out.Len()) {
		t.Fatalf("byte count %d != written %d", n, out.Len())
	}
	script := out.String()
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	// 流式路径每个 emit 批次独立成一条 INSERT（mysqldump extended-insert
	// 分段形态）：fake 首帧 = cols + 两行 → 一条两元组 INSERT，分号在批内。
	for _, want := range []string{
		"DROP TABLE IF EXISTS `users`;",
		"CREATE TABLE `users`",
		"INSERT INTO `users` (`id`, `name`) VALUES\n  ('1', 'a''b'),\n  ('2', NULL);",
		"SET FOREIGN_KEY_CHECKS=1;",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("dump missing %q\n---\n%s", want, script)
		}
	}
	// 行数守卫：dump 出的元组数必须等于源行数——cols 帧吞首批判曾经全绿通过。
	if got := strings.Count(script, "\n  ("); got != 2 {
		t.Fatalf("dumped %d row tuples, want 2\n---\n%s", got, script)
	}
	if strings.Contains(script, "`v`") {
		t.Fatalf("view should be skipped:\n%s", script)
	}
	// 流式路径：数据查询必须走 QueryStream，而不是物化的 SELECT 直读。
	// fakeSession 同时实现了两种；dumpDatabase 应选 Streamer。
	if len(sess.streamed) == 0 {
		t.Fatal("data dump must go through QueryStream")
	}
	for _, q := range sess.ran {
		if strings.HasPrefix(q, "SELECT * FROM") {
			t.Fatalf("data must stream via QueryStream, got materialized Query: %q", q)
		}
	}
}

// multiBatchSession 复用 fakeSession 的目录/DDL 桩，把数据流拆成多帧：
// 首帧 cols+首行，次帧一行——覆盖大于一个 batch 的表与 keep=false 的传播。
// 有意覆盖（而非继承）QueryStream：嵌入会让 fakeSession 的单帧版本遮住本方法。
type multiBatchSession struct {
	fakeSession
}

func (f *multiBatchSession) QueryStream(sql string, batch int, emit func(cols []proto.ColumnInfo, rows [][]*string) (bool, error)) (proto.QueryResult, error) {
	f.streamed = append(f.streamed, sql)
	if !strings.HasPrefix(sql, "SELECT * FROM") {
		return proto.QueryResult{}, nil
	}
	s := func(v string) *string { return &v }
	cols := []proto.ColumnInfo{{Name: "id"}, {Name: "name"}}
	keep, err := emit(cols, [][]*string{{s("1"), s("a'b")}})
	if err != nil || !keep {
		return proto.QueryResult{}, err
	}
	keep, err = emit(nil, [][]*string{{s("2"), nil}})
	if err != nil || !keep {
		return proto.QueryResult{}, err
	}
	return proto.QueryResult{}, nil
}

func TestDumpDatabaseStreamingBatches(t *testing.T) {
	sess := &multiBatchSession{}
	var out strings.Builder
	_, warnings, err := dumpDatabase(sess, "mysql", "shop", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	script := out.String()
	// 多帧路径：每个 emit 批次独立成一条 INSERT（extended-insert 分段形态）。
	for _, want := range []string{
		"INSERT INTO `users` (`id`, `name`) VALUES\n  ('1', 'a''b');",
		"INSERT INTO `users` (`id`, `name`) VALUES\n  ('2', NULL);",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("dump missing %q\n---\n%s", want, script)
		}
	}
	if got := strings.Count(script, "\n  ("); got != 2 {
		t.Fatalf("dumped %d row tuples, want 2\n---\n%s", got, script)
	}
}

func TestDumpTableStreamingCapWritesIncompleteMarker(t *testing.T) {
	defer func(origLimit, origBatch int) { dumpRowLimit, dumpBatch = origLimit, origBatch }(dumpRowLimit, dumpBatch)
	dumpRowLimit, dumpBatch = 1, 200

	// 两行 > cap=1：首帧（cols+首行）写出 1 行，次帧触发 room<=0 截断。
	// 真实 cap 落在批次边界（10 万 % 200 == 0），走的就是这条 room<=0 路径。
	sess := &multiBatchSession{}
	var out strings.Builder
	var warnings []string
	if err := dumpTableDataStreaming(sess, "mysql", "users", "`shop`.`users`",
		func(s string) error { out.WriteString(s); return nil }, &warnings); err != nil {
		t.Fatal(err)
	}
	script := out.String()
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %v", warnings)
	}
	// 截断必须落在 dump 文件本身，否则恢复文件看起来是完整的。
	if !strings.Contains(script, "INCOMPLETE") {
		t.Fatalf("truncation must be visible in the dump itself:\n%s", script)
	}
	if got := strings.Count(script, "\n  ("); got != 1 {
		t.Fatalf("dumped %d row tuples, want 1 (the cap)\n---\n%s", got, script)
	}
	// 标记在最后一条完整 INSERT 之后（段间空行相隔），语句自身仍自洽。
	if !strings.Contains(script, "('1', 'a''b');\n\n-- WARNING: users row dump hit the 1 cap") {
		t.Fatalf("marker must follow the last complete INSERT:\n%s", script)
	}
}

// nonStreamSession 是没有 Streamer 能力的会话（旧插件形态）：备份退回物化
// 路径，dump 仍须正确。独立类型不嵌 fakeSession——Go 的方法集提升会让嵌入
// 类型"继承"QueryStream，从而测不到回退路径。
type nonStreamSession struct {
	ran []string
}

func (f *nonStreamSession) Server() string                             { return "fake" }
func (f *nonStreamSession) Tables() ([]proto.ObjectInfo, error)        { return nil, nil }
func (f *nonStreamSession) Columns(string) ([]proto.ColumnInfo, error) { return nil, nil }
func (f *nonStreamSession) Browse(string, int) (proto.QueryResult, error) {
	return proto.QueryResult{}, nil
}
func (f *nonStreamSession) Close() {}

func (f *nonStreamSession) Query(sql string, _ int) (proto.QueryResult, error) {
	f.ran = append(f.ran, sql)
	s := func(v string) proto.CellPayload { return proto.CellPayload{Kind: "text", Text: v} }
	switch {
	case strings.HasPrefix(sql, "SHOW FULL TABLES"):
		return proto.QueryResult{Rows: [][]proto.CellPayload{{s("users"), s("BASE TABLE")}}}, nil
	case strings.HasPrefix(sql, "SHOW CREATE TABLE"):
		return proto.QueryResult{Rows: [][]proto.CellPayload{{s("users"), s("CREATE TABLE `users` (`id` int, `name` varchar(20))")}}}, nil
	case strings.HasPrefix(sql, "SELECT * FROM"):
		return proto.QueryResult{
			Columns: []proto.ColumnInfo{{Name: "id"}, {Name: "name"}},
			Rows: [][]proto.CellPayload{
				{s("1"), s("a'b")},
				{s("2"), {Kind: "null"}},
			},
		}, nil
	}
	return proto.QueryResult{}, nil
}

func TestDumpDatabaseMaterializedFallback(t *testing.T) {
	sess := &nonStreamSession{}
	var out strings.Builder
	_, warnings, err := dumpDatabase(sess, "mysql", "shop", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	script := out.String()
	for _, want := range []string{
		// 物化回退：StringRows() 全量后按 dumpBatch 分批，fake 数据只有
		// 2 行 → 一条 INSERT 两行元组。
		"INSERT INTO `users` (`id`, `name`) VALUES\n  ('1', 'a''b'),\n  ('2', NULL);",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("fallback dump missing %q\n---\n%s", want, script)
		}
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

func TestSplitStatementsDelimited(t *testing.T) {
	script := "SET FOREIGN_KEY_CHECKS=0;\n" +
		"DELIMITER ;;\n" +
		"CREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND;;\n" +
		"DELIMITER ;\n" +
		"SET FOREIGN_KEY_CHECKS=1;\n"
	got := splitStatementsDelimited(script)
	want := []string{
		"SET FOREIGN_KEY_CHECKS=0",
		"CREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND",
		"SET FOREIGN_KEY_CHECKS=1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d stmts: %#v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stmt %d:\n%q\nwant\n%q", i, got[i], want[i])
		}
	}
}

func TestSplitStatementsDelimitedBlockComments(t *testing.T) {
	script := "/* intro; with a semicolon */ SET a=1;\n" +
		"/*!40101 SET @saved_cs_client = @@character_set_client */;\n" +
		"SELECT 'x;/*not-comment*/y' AS s;\n" +
		"/* trailing unterminated comment"
	got := splitStatementsDelimited(script)
	want := []string{
		"SET a=1",
		"/*!40101 SET @saved_cs_client = @@character_set_client */",
		"SELECT 'x;/*not-comment*/y' AS s",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d stmts: %#v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stmt %d:\n%q\nwant\n%q", i, got[i], want[i])
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

// A NUL byte inside a cell must be escaped, not emitted raw: MySQL's parser
// ends a text literal at a raw 0x00, so a dumped BLOB-shaped value would be
// silently truncated on restore. `\0` is the escape for it (SEC-03 同类).
func TestJoinMySQLValuesEscapesNUL(t *testing.T) {
	withNUL := "a\x00b"
	got := joinMySQLValues([]*string{&withNUL})
	if strings.ContainsRune(got, 0) {
		t.Fatalf("raw NUL reached the dump: %q", got)
	}
	if want := `'a\0b'`; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// A backslash before the NUL must still be escaped exactly once (the NUL
	// escape is inserted after the backslash pass, so it is not re-escaped).
	both := "\\\x00"
	if got := joinMySQLValues([]*string{&both}); got != `'\\\0'` {
		t.Fatalf("backslash+NUL = %q, want %q", got, `'\\\0'`)
	}
}

func TestQuoteIdentFallbackUsesDialectDelimiters(t *testing.T) {
	// QuoteIdent rejects empty / overlong / control-carrying names; the backup
	// and DDL paths still render them. The fallback must keep the fragment a
	// valid identifier *for that dialect* — the old unconditional ANSI
	// double-quote fallback is a string literal on MySQL, so `"db".table`
	// silently stops being an identifier instead of erroring.
	cases := []struct {
		driver string
		name   string
		want   string
	}{
		{"mysql", "a`b", "`a``b`"},
		{"mysql", strings.Repeat("x", 300), "`" + strings.Repeat("x", 300) + "`"},
		{"mysql", "a\x00b", "`a\x00b`"},
		{"postgres", strings.Repeat("x", 300), `"` + strings.Repeat("x", 300) + `"`},
		{"sqlserver", "a]b", "[a]]b]"},
		{"unknown-driver", "a`b", "\"a`b\""},
	}
	for _, c := range cases {
		if got := quoteIdent(c.driver, c.name); got != c.want {
			t.Errorf("quoteIdent(%q, %q) = %q, want %q", c.driver, c.name, got, c.want)
		}
		if got := quoteCol(c.name, c.driver); got != c.want {
			t.Errorf("quoteCol(%q, %q) = %q, want %q", c.name, c.driver, got, c.want)
		}
	}
}

// The client's identifier quoting must stay single-sourced: for every driver
// spelling the kernel knows (plus aliases and one it does not) and a battery of
// names, the client must agree with sqltext — both on the valid names (guarded
// path) and on the names QuoteIdent refuses (loose path).
func TestQuoteIdentMatchesSqltext(t *testing.T) {
	drivers := append([]string{}, sqltext.Engines...)
	drivers = append(drivers, "mariadb", "mssql", "postgres", "pg", "oracle", "nope")
	names := []string{"plain", "a`b", `a"b`, "a]b", "", strings.Repeat("x", 300), "a\x00b", "a\nb"}

	for _, driver := range drivers {
		for _, name := range names {
			loose := sqltext.QuoteIdentLoose(driver, name)
			if got := quoteIdent(driver, name); got != loose {
				t.Errorf("quoteIdent(%q, %q) = %q, want %q (sqltext)", name, driver, got, loose)
			}
			if got := quoteCol(name, driver); got != loose {
				t.Errorf("quoteCol(%q, %q) = %q, want %q (sqltext)", name, driver, got, loose)
			}
			// Valid names must agree with the guarded path, not just the loose one.
			if q, err := sqltext.QuoteIdent(driver, name); err == nil && q != quoteIdent(driver, name) {
				t.Errorf("quoteIdent(%q, %q) = %q, want guarded %q", name, driver, quoteIdent(driver, name), q)
			}
		}
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
