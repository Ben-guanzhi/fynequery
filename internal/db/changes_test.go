package db

import (
	"strings"
	"testing"
)

// Engine tests ported from the parent TableProGo project's changes_test.go,

// adapted to fynequery's internal/db package.

func planColumns() []string { return []string{"id", "name", "amount"} }

func planTypes() map[string]string {
	return map[string]string{"id": "int", "name": "varchar(50)", "amount": "decimal(18,2)"}
}

func planChanges(edits ...CellEdit) []RowChange {
	return []RowChange{{Original: []interface{}{7.0, "old", "1.50"}, Edits: edits}}
}

func TestUpdateUsesKeyColumnsAndDialectQuoting(t *testing.T) {
	statements, err := BuildStatements("sqlserver", "[zs].[dbo].[t]", planColumns(),
		[]string{"id"}, planTypes(), planChanges(CellEdit{Column: "name", Value: "O'Brien"}))
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	want := "UPDATE [zs].[dbo].[t] SET [name] = N'O''Brien' WHERE [id] = 7"
	if statements[0] != want {
		t.Errorf("got  %s\nwant %s", statements[0], want)
	}

	mysql, err := BuildStatements("mysql", "`zs`.`t`", planColumns(), []string{"id"},
		planTypes(), planChanges(CellEdit{Column: "amount", Value: "2.75"}))
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if mysql[0] != "UPDATE `zs`.`t` SET `amount` = 2.75 WHERE `id` = 7" {
		t.Errorf("unexpected mysql statement: %s", mysql[0])
	}
}

func TestUpdateNeedsEveryKeyInTheResult(t *testing.T) {
	_, err := BuildStatements("sqlserver", "[t]", []string{"name"}, []string{"id"},
		planTypes(), planChanges(CellEdit{Column: "name", Value: "x"}))
	if err == nil || !strings.Contains(err.Error(), "does not include key column id") {
		t.Errorf("missing key column should be refused, got %v", err)
	}
}

func TestUpdateRefusesKeylessTables(t *testing.T) {
	_, err := BuildStatements("sqlserver", "[t]", planColumns(), nil, planTypes(),
		planChanges(CellEdit{Column: "name", Value: "x"}))
	if err == nil || !strings.Contains(err.Error(), "no primary key") {
		t.Errorf("a table without a key must not be editable, got %v", err)
	}
}

func TestUpdateRefusesColumnsTheQueryInvented(t *testing.T) {
	// "amount2" is a projection, not a field of the table, so no UPDATE may name it.
	_, err := BuildStatements("sqlserver", "[t]", planColumns(), []string{"id"},
		planTypes(), planChanges(CellEdit{Column: "amount2", Value: "1"}))
	if err == nil || !strings.Contains(err.Error(), "not in the result") {
		t.Errorf("unknown result column should be refused, got %v", err)
	}

	types := planTypes()
	delete(types, "amount")
	_, err = BuildStatements("sqlserver", "[t]", planColumns(), []string{"id"}, types,
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
	statements, err := BuildStatements("sqlite", `"t"`, []string{"a", "b"}, []string{"a", "b"},
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
	statements, err := BuildStatements("mysql", "`t`", []string{"id", "name"}, []string{"id"},
		planTypes(), []RowChange{{Kind: RowInsert, Edits: []CellEdit{{Column: "name", Value: "new"}}}})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if statements[0] != "INSERT INTO `t` (`name`) VALUES ('new')" {
		t.Errorf("unexpected insert: %s", statements[0])
	}
}

func TestDeleteUsesKeyPredicate(t *testing.T) {
	statements, err := BuildStatements("postgresql", `"public"."t"`, planColumns(),
		[]string{"id"}, planTypes(), []RowChange{{Kind: RowDelete, Original: []interface{}{3.0}}})
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
		"SELECT * FROM users":       {Database: "db", Table: "users"},
		"select * from dbo.orders;": {Database: "db", Schema: "dbo", Table: "orders"},
		// A two-part ref is schema.table against the fallback database.
		"SELECT * FROM `mydb`.`t`":                  {Database: "db", Schema: "mydb", Table: "t"},
		"  SELECT DISTINCT * FROM [srv].[db].[t]  ": {Database: "srv", Schema: "db", Table: "t"},
		"SELECT * FROM users LIMIT 10":              {Database: "db", Table: "users"},
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

func TestInsertScriptExport(t *testing.T) {
	script, err := InsertScript("sqlserver", "[dbo].[t]", "t", []string{"id", "name"},
		planTypes(), [][]interface{}{{1.0, "O'Brien"}, {2.0, nil}})
	if err != nil {
		t.Fatalf("InsertScript: %v", err)
	}
	want := "INSERT INTO [dbo].[t] ([id], [name]) VALUES (1, N'O''Brien');\n" +
		"INSERT INTO [dbo].[t] ([id], [name]) VALUES (2, NULL);\n"
	if script != want {
		t.Errorf("script =\n%s\nwant\n%s", script, want)
	}
	if _, err := InsertScript("mysql", "`t`", "t", []string{"id", "alias"},
		planTypes(), [][]interface{}{{1.0, "x"}}); err == nil {
		t.Error("a result column that is not a table field must be refused")
	}
}

// TestInsertScriptMaxRows pins the export cap boundary: exactly
// insertExportMaxRows rows are accepted and one past it is refused. The check
// sits on the rows slice before rendering, so the refusal is instant rather than
// arriving after the work of rendering nine thousand nine hundred and ninety-nine
// statements.
func TestInsertScriptMaxRows(t *testing.T) {
	cols := []string{"id"}
	rows := make([][]interface{}, insertExportMaxRows+1)
	for i := range rows {
		rows[i] = []interface{}{float64(i)}
	}
	if _, err := InsertScript(DbTypeSQLite, "t", "t", cols, planTypes(),
		rows[:insertExportMaxRows]); err != nil {
		t.Fatalf("exactly %d rows must export: %v", insertExportMaxRows, err)
	}
	if _, err := InsertScript(DbTypeSQLite, "t", "t", cols, planTypes(), rows); err == nil {
		t.Fatal("one row past the cap must be refused")
	}
}
