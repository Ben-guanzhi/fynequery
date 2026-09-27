package main

import (
	"strings"
	"testing"
)

// Engine tests ported from the parent TableProGo project's changes_test.go,
// adapted to fynequery's Client.

func planColumns() []string { return []string{"id", "name", "amount"} }
func planTypes() map[string]string {
	return map[string]string{"id": "int", "name": "varchar(50)", "amount": "decimal(18,2)"}
}

func planChanges(edits ...CellEdit) []RowChange {
	return []RowChange{{Original: []interface{}{7.0, "old", "1.50"}, Edits: edits}}
}

func TestUpdateUsesKeyColumnsAndDialectQuoting(t *testing.T) {
	statements, err := buildStatements("sqlserver", "[zs].[dbo].[t]", planColumns(),
		[]string{"id"}, planTypes(), planChanges(CellEdit{Column: "name", Value: "O'Brien"}))
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	want := "UPDATE [zs].[dbo].[t] SET [name] = N'O''Brien' WHERE [id] = 7"
	if statements[0] != want {
		t.Errorf("got  %s\nwant %s", statements[0], want)
	}

	mysql, err := buildStatements("mysql", "`zs`.`t`", planColumns(), []string{"id"},
		planTypes(), planChanges(CellEdit{Column: "amount", Value: "2.75"}))
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if mysql[0] != "UPDATE `zs`.`t` SET `amount` = 2.75 WHERE `id` = 7" {
		t.Errorf("unexpected mysql statement: %s", mysql[0])
	}
}

func TestUpdateNeedsEveryKeyInTheResult(t *testing.T) {
	_, err := buildStatements("sqlserver", "[t]", []string{"name"}, []string{"id"},
		planTypes(), planChanges(CellEdit{Column: "name", Value: "x"}))
	if err == nil || !strings.Contains(err.Error(), "does not include key column id") {
		t.Errorf("missing key column should be refused, got %v", err)
	}
}

func TestUpdateRefusesKeylessTables(t *testing.T) {
	_, err := buildStatements("sqlserver", "[t]", planColumns(), nil, planTypes(),
		planChanges(CellEdit{Column: "name", Value: "x"}))
	if err == nil || !strings.Contains(err.Error(), "no primary key") {
		t.Errorf("a table without a key must not be editable, got %v", err)
	}
}

func TestUpdateRefusesColumnsTheQueryInvented(t *testing.T) {
	// "amount2" is a projection, not a field of the table, so no UPDATE may name it.
	_, err := buildStatements("sqlserver", "[t]", planColumns(), []string{"id"},
		planTypes(), planChanges(CellEdit{Column: "amount2", Value: "1"}))
	if err == nil || !strings.Contains(err.Error(), "not in the result") {
		t.Errorf("unknown result column should be refused, got %v", err)
	}

	types := planTypes()
	delete(types, "amount")
	_, err = buildStatements("sqlserver", "[t]", planColumns(), []string{"id"}, types,
		planChanges(CellEdit{Column: "amount", Value: "1"}))
	if err == nil || !strings.Contains(err.Error(), "not a plain column") {
		t.Errorf("a column missing from the table should be refused, got %v", err)
	}
}

func TestValueLiteralsFollowTheColumn(t *testing.T) {
	cases := []struct {
		dbType, colType string
		value           interface{}
		want            string
	}{
		{"postgresql", "varchar", nil, "NULL"},
		{"postgresql", "int", 42.0, "42"},
		{"postgresql", "int", " 42 ", "42"},
		{"postgresql", "int", "", "NULL"},
		{"postgresql", "bigint", 1e18, "1000000000000000000"},
		{"postgresql", "double precision", 0.5, "0.5"},
		{"postgresql", "text", "it's", "'it''s'"},
		{"sqlserver", "nvarchar", "中文", "N'中文'"},
		{"postgresql", "character varying(20)", "x", "'x'"},
		{"oracle", "varchar2", "x", "'x'"},
		{"postgresql", "boolean", true, "1"},
	}
	for _, c := range cases {
		got, err := valueLiteral(c.dbType, c.colType, c.value)
		if err != nil {
			t.Errorf("valueLiteral(%s, %v): %v", c.colType, c.value, err)
			continue
		}
		if got != c.want {
			t.Errorf("valueLiteral(%s, %s, %v) = %s, want %s", c.dbType, c.colType, c.value, got, c.want)
		}
	}

	if _, err := valueLiteral("postgresql", "int", "twelve"); err == nil {
		t.Error("text in a numeric column must be refused, not written as a guess")
	}
	if _, err := valueLiteral("postgresql", "text", "a\x00b"); err == nil {
		t.Error("NUL must not reach the server")
	}
}

func TestKeyOfNULLIsComparedAsNULL(t *testing.T) {
	statements, err := buildStatements("sqlite", `"t"`, []string{"a", "b"}, []string{"a", "b"},
		map[string]string{"a": "int", "b": "text"},
		[]RowChange{{Original: []interface{}{nil, nil}, Edits: []CellEdit{{Column: "a", Value: 1.0}}}})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if statements[0] != `UPDATE "t" SET "a" = 1 WHERE "a" IS NULL AND "b" IS NULL` {
		t.Errorf("unexpected statement: %s", statements[0])
	}
}

func TestInsertWritesOnlyFilledColumns(t *testing.T) {
	statements, err := buildStatements("mysql", "`t`", []string{"id", "name"}, []string{"id"},
		planTypes(), []RowChange{{Kind: rowInsert, Edits: []CellEdit{{Column: "name", Value: "new"}}}})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if statements[0] != "INSERT INTO `t` (`name`) VALUES ('new')" {
		t.Errorf("unexpected insert: %s", statements[0])
	}
}

func TestDeleteUsesKeyPredicate(t *testing.T) {
	statements, err := buildStatements("postgresql", `"public"."t"`, planColumns(),
		[]string{"id"}, planTypes(), []RowChange{{Kind: rowDelete, Original: []interface{}{3.0}}})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if statements[0] != `DELETE FROM "public"."t" WHERE "id" = 3` {
		t.Errorf("unexpected delete: %s", statements[0])
	}
}

// TestParseQuerySource checks which results the grid may treat as editable:
// exactly one table, star-selected, no joins or grouping.
func TestParseQuerySource(t *testing.T) {
	editable := map[string]TableRef{
		"SELECT * FROM users":          {Database: "db", Table: "users"},
		"select * from dbo.orders;":    {Database: "db", Schema: "dbo", Table: "orders"},
		// A two-part ref is schema.table against the fallback database.
		"SELECT * FROM `mydb`.`t`":     {Database: "db", Schema: "mydb", Table: "t"},
		"  SELECT DISTINCT * FROM [srv].[db].[t]  ": {Database: "srv", Schema: "db", Table: "t"},
		"SELECT * FROM users LIMIT 10": {Database: "db", Table: "users"},
	}
	for q, want := range editable {
		got := parseQuerySource("c0", q, "db")
		if got.Table != want.Table || got.Schema != want.Schema || got.Database != want.Database {
			t.Errorf("parseQuerySource(%q) = %+v, want %+v", q, got, want)
		}
	}
	readOnly := []string{
		"SELECT id, name FROM users",          // projection
		"SELECT COUNT(*) FROM users",          // aggregate
		"SELECT * FROM a JOIN b ON a.id=b.id", // join
		"SELECT * FROM a GROUP BY id",         // grouping
		"UPDATE users SET name = 'x'",         // not a select
		"SELECT * FROM",                       // no table
	}
	for _, q := range readOnly {
		if got := parseQuerySource("c0", q, "db"); got.Table != "" {
			t.Errorf("parseQuerySource(%q) = %+v, want read-only", q, got)
		}
	}
}

// TestCollectChangesOrderAndNULL checks the UI queue → RowChange conversion:
// statements in queue order, empty queued values become NULL, deleted rows
// that were also edited only produce a DELETE.
func TestCollectChangesOrderAndNULL(t *testing.T) {
	c := &Client{
		gridCols: []string{"id", "name"},
		lastRows: [][]interface{}{
			{1.0, "a"},
			{2.0, "b"},
			{3.0, "c"},
		},
		edits: newGridEditState(),
	}
	c.edits.setUpdate(2, "name", "x")
	c.edits.setUpdate(0, "name", "") // NULL
	c.edits.deletes[0] = true
	c.edits.inserts = append(c.edits.inserts, map[string]string{"name": "n", "id": "9"})

	changes := c.collectChanges()
	// Queue order: updates by row, deletes by row, then new rows.
	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3", len(changes))
	}
	if changes[0].Kind != rowUpdate || changes[0].Original[0] != 3.0 {
		t.Errorf("first change = %+v, want update of row 2", changes[0])
	}
	if changes[1].Kind != rowDelete || changes[1].Original[0] != 1.0 {
		t.Errorf("second change = %+v, want delete of row 0", changes[1])
	}
	if changes[2].Kind != rowInsert {
		t.Errorf("third change = %+v, want insert", changes[2])
	}
	ins := RowChange{Kind: rowInsert, Edits: c.editsOf(c.edits.inserts[0])}
	statements, err := buildStatements("mysql", "`t`", c.gridCols, []string{"id"},
		planTypes(), []RowChange{ins})
	if err != nil {
		t.Fatalf("insert build: %v", err)
	}
	if statements[0] != "INSERT INTO `t` (`id`, `name`) VALUES (9, 'n')" {
		t.Errorf("unexpected insert: %s", statements[0])
	}
}

func TestInsertScriptExport(t *testing.T) {
	script, err := insertScript("sqlserver", "[dbo].[t]", "t", []string{"id", "name"},
		planTypes(), [][]interface{}{{1.0, "O'Brien"}, {2.0, nil}})
	if err != nil {
		t.Fatalf("insertScript: %v", err)
	}
	want := "INSERT INTO [dbo].[t] ([id], [name]) VALUES (1, N'O''Brien');\n" +
		"INSERT INTO [dbo].[t] ([id], [name]) VALUES (2, NULL);\n"
	if script != want {
		t.Errorf("script =\n%s\nwant\n%s", script, want)
	}
	if _, err := insertScript("mysql", "`t`", "t", []string{"id", "alias"},
		planTypes(), [][]interface{}{{1.0, "x"}}); err == nil {
		t.Error("a result column that is not a table field must be refused")
	}
}
