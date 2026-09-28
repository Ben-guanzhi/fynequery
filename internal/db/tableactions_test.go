package db

import (
	"strings"
	"testing"
)

func actionStatement(t *testing.T, o ObjectNames, action string) string {
	t.Helper()
	statement, err := TableActionStatement(o, action)
	if err != nil {
		t.Fatalf("%s failed: %v", action, err)
	}
	return statement
}

func TestTableActionStatementsByDialect(t *testing.T) {
	cases := []struct {
		name     string
		object   ObjectNames
		action   string
		expected string
	}{
		{"drop a sqlite table", ObjectNames{DbType: "sqlite", ObjectType: "table", Table: "parts"}, ActionDrop, `DROP TABLE "parts"`},
		{"drop a mysql view", ObjectNames{DbType: "mysql", ObjectType: "view", Database: "shop", Table: "cheap"}, ActionDrop, "DROP VIEW `shop`.`cheap`"},
		{"truncate keeps the schema", ObjectNames{DbType: "sqlserver", ObjectType: "table", Database: "erp", Schema: "sales", Table: "orders"}, ActionTruncate, "TRUNCATE TABLE [erp].[sales].[orders]"},
		// SQLite has no TRUNCATE; the CLI's own way is a DELETE with no
		// condition, which leaves the autoincrement counter alone.
		{"sqlite clears with a delete", ObjectNames{DbType: "sqlite", ObjectType: "table", Table: "parts"}, ActionTruncate, `DELETE FROM "parts"`},
		{"mysql renames both sides", ObjectNames{DbType: "mysql", ObjectType: "table", Database: "shop", Table: "parts", NewName: "pieces"}, ActionRename, "RENAME TABLE `shop`.`parts` TO `shop`.`pieces`"},
		{"sql server renames through its procedure", ObjectNames{DbType: "sqlserver", ObjectType: "table", Database: "erp", Schema: "sales", Table: "orders", NewName: "history"}, ActionRename, `EXEC sp_rename N'sales.orders', N'history'`},
		{"postgres renames within its schema", ObjectNames{DbType: "postgresql", ObjectType: "table", Schema: "sales", Table: "orders", NewName: "history"}, ActionRename, `ALTER TABLE "sales"."orders" RENAME TO "history"`},
		{"a quoted name survives the quoting", ObjectNames{DbType: "postgresql", ObjectType: "table", Schema: "public", Table: `we"ird`, NewName: "fine"}, ActionDrop, `DROP TABLE "public"."we""ird"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := actionStatement(t, tc.object, tc.action); got != tc.expected {
				t.Errorf("want %s\ngot  %s", tc.expected, got)
			}
		})
	}
}

func TestTableActionRefusals(t *testing.T) {
	if _, err := TableActionStatement(ObjectNames{DbType: "mysql", ObjectType: "view", Database: "d", Table: "v"}, ActionTruncate); err == nil {
		t.Error("a view has nothing to truncate")
	}
	if _, err := TableActionStatement(ObjectNames{DbType: "mysql", ObjectType: "table", Database: "d", Table: "t", NewName: "t"}, ActionRename); err == nil {
		t.Error("renaming to the same name must be refused")
	}
	if _, err := TableActionStatement(ObjectNames{DbType: "mysql", ObjectType: "table", Database: "d", Table: "t", NewName: "1 bad"}, ActionRename); err == nil {
		t.Error("a non-identifier new name must be refused")
	}
	if _, err := TableActionStatement(ObjectNames{DbType: "mysql", ObjectType: "table", Database: "d", Table: "t"}, "explode"); err == nil {
		t.Error("an unknown action must be refused")
	}
}

// TestFilterConditions checks the rendered WHERE text: escaping, numeric
// refusal, and the empty-value-means-NULL semantics.
func TestFilterConditions(t *testing.T) {
	cases := []struct {
		name   string
		dbType string
		colTyp string
		filter TableFilter
		want   string
	}{
		{"text equals", "mysql", "varchar(50)", TableFilter{"name", FilterEq, "O'Brien"}, "`name` = 'O''Brien'"},
		{"numeric equals", "postgresql", "int", TableFilter{"age", FilterEq, "42"}, `"age" = 42`},
		{"numeric text coercion", "postgresql", "bigint", TableFilter{"n", FilterGTE, " 100 "}, `"n" >= 100`},
		{"empty numeric is null", "postgresql", "int", TableFilter{"age", FilterEq, ""}, `"age" IS NULL`},
		{"empty numeric ne", "postgresql", "int", TableFilter{"age", FilterNe, ""}, `"age" IS NOT NULL`},
		{"is null", "sqlite", "text", TableFilter{"a", FilterIsNull, ""}, `"a" IS NULL`},
	}
	for _, tc := range cases {
		quoted, err := quoteIdent(tc.dbType, tc.filter.Column)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, err := filterCondition(tc.dbType, tc.colTyp, quoted, tc.filter)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}

	// "100%" must search for the literal text: the typed % is escaped with /
	// and only the outer pair stays wild.
	if got := ContainsPattern("100%"); got != "%100/%%" {
		t.Errorf("ContainsPattern = %q, want %q", got, "%100/%%")
	}
	if _, err := filterCondition("mysql", "int", "`n`", TableFilter{"n", FilterContains, "1"}); err == nil {
		t.Error("contains on a numeric column must be refused")
	}
	if _, err := filterCondition("postgresql", "int", `"n"`, TableFilter{"n", FilterLT, ""}); err == nil {
		t.Error("a range comparison against nothing must be refused")
	}
}

// TestForeignKeyHelpers covers the pure half of the FK picker.
func TestForeignKeyHelpers(t *testing.T) {
	fks := []ForeignKeyInfo{
		{Name: "fk_a", Columns: []string{"user_id"}, RefSchema: "dbo", RefTable: "users", RefColumns: []string{"id"}},
		{Name: "fk_b", Columns: []string{"a", "b"}, RefTable: "pair", RefColumns: []string{"x", "y"}},
	}
	fk, err := singleColumnForeignKey(fks, "USER_ID")
	if err != nil || fk.Name != "fk_a" {
		t.Fatalf("singleColumnForeignKey = %v, %v", fk, err)
	}
	if _, err := singleColumnForeignKey(fks, "a"); err == nil {
		t.Error("a composite key must be refused")
	}
	if _, err := singleColumnForeignKey(fks, "other"); err == nil {
		t.Error("a non-fk column must be refused")
	}

	columns := []ColumnInfo{
		{Name: "id", Type: "int", IsPrimaryKey: true},
		{Name: "created", Type: "datetime"},
		{Name: "real_name", Type: "nvarchar(50)"},
	}
	if got := pickLabelColumn(columns, "id"); got != "real_name" {
		t.Errorf("pickLabelColumn = %q, want real_name", got)
	}
	if got := pickLabelColumn(columns, "REAL_NAME"); got != "" {
		t.Errorf("the ref column itself must not be its own label, got %q", got)
	}

	if got, _ := fkValuesQuery("sqlite", `"users"`, "id", "", 10); !strings.Contains(got, "LIMIT 10") {
		t.Errorf("sqlite fk query = %s", got)
	}
	if got, _ := fkValuesQuery("oracle", `"USERS"`, "ID", "NAME", 10); !strings.Contains(got, "FETCH NEXT 10 ROWS ONLY") {
		t.Errorf("oracle fk query = %s", got)
	}
	if _, err := fkValuesQuery("mysql", "`users`", "id", "", 0); err == nil {
		t.Error("a zero limit is invalid")
	}

	// The target name follows the dialect: mysql uses database as schema.
	if got, err := fkTargetName("mysql", "shop", "", "users"); err != nil || got != "`shop`.`users`" {
		t.Errorf("fkTargetName mysql = %s, %v", got, err)
	}
	if got, err := fkTargetName("postgresql", "shop", "", "users"); err != nil || got != `"public"."users"` {
		t.Errorf("fkTargetName postgres = %s, %v", got, err)
	}
}
