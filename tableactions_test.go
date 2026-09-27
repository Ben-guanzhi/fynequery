package main

import (
	"strings"
	"testing"
)

func actionStatement(t *testing.T, o objectNames, action string) string {
	t.Helper()
	statement, err := tableActionStatement(o, action)
	if err != nil {
		t.Fatalf("%s failed: %v", action, err)
	}
	return statement
}

func TestTableActionStatementsByDialect(t *testing.T) {
	cases := []struct {
		name     string
		object   objectNames
		action   string
		expected string
	}{
		{"drop a sqlite table", objectNames{dbType: "sqlite", objectType: "table", table: "parts"}, actionDrop, `DROP TABLE "parts"`},
		{"drop a mysql view", objectNames{dbType: "mysql", objectType: "view", database: "shop", table: "cheap"}, actionDrop, "DROP VIEW `shop`.`cheap`"},
		{"truncate keeps the schema", objectNames{dbType: "sqlserver", objectType: "table", database: "erp", schema: "sales", table: "orders"}, actionTruncate, "TRUNCATE TABLE [erp].[sales].[orders]"},
		// SQLite has no TRUNCATE; the CLI's own way is a DELETE with no
		// condition, which leaves the autoincrement counter alone.
		{"sqlite clears with a delete", objectNames{dbType: "sqlite", objectType: "table", table: "parts"}, actionTruncate, `DELETE FROM "parts"`},
		{"mysql renames both sides", objectNames{dbType: "mysql", objectType: "table", database: "shop", table: "parts", newName: "pieces"}, actionRename, "RENAME TABLE `shop`.`parts` TO `shop`.`pieces`"},
		{"sql server renames through its procedure", objectNames{dbType: "sqlserver", objectType: "table", database: "erp", schema: "sales", table: "orders", newName: "history"}, actionRename, `EXEC sp_rename N'sales.orders', N'history'`},
		{"postgres renames within its schema", objectNames{dbType: "postgresql", objectType: "table", schema: "sales", table: "orders", newName: "history"}, actionRename, `ALTER TABLE "sales"."orders" RENAME TO "history"`},
		{"a quoted name survives the quoting", objectNames{dbType: "postgresql", objectType: "table", schema: "public", table: `we"ird`, newName: "fine"}, actionDrop, `DROP TABLE "public"."we""ird"`},
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
	if _, err := tableActionStatement(objectNames{dbType: "mysql", objectType: "view", database: "d", table: "v"}, actionTruncate); err == nil {
		t.Error("a view has nothing to truncate")
	}
	if _, err := tableActionStatement(objectNames{dbType: "mysql", objectType: "table", database: "d", table: "t", newName: "t"}, actionRename); err == nil {
		t.Error("renaming to the same name must be refused")
	}
	if _, err := tableActionStatement(objectNames{dbType: "mysql", objectType: "table", database: "d", table: "t", newName: "1 bad"}, actionRename); err == nil {
		t.Error("a non-identifier new name must be refused")
	}
	if _, err := tableActionStatement(objectNames{dbType: "mysql", objectType: "table", database: "d", table: "t"}, "explode"); err == nil {
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
		{"text equals", "mysql", "varchar(50)", TableFilter{"name", filterEq, "O'Brien"}, "`name` = 'O''Brien'"},
		{"numeric equals", "postgresql", "int", TableFilter{"age", filterEq, "42"}, `"age" = 42`},
		{"numeric text coercion", "postgresql", "bigint", TableFilter{"n", filterGTE, " 100 "}, `"n" >= 100`},
		{"empty numeric is null", "postgresql", "int", TableFilter{"age", filterEq, ""}, `"age" IS NULL`},
		{"empty numeric ne", "postgresql", "int", TableFilter{"age", filterNe, ""}, `"age" IS NOT NULL`},
		{"is null", "sqlite", "text", TableFilter{"a", filterIsNull, ""}, `"a" IS NULL`},
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
	if got := containsPattern("100%"); got != "%100/%%" {
		t.Errorf("containsPattern = %q, want %q", got, "%100/%%")
	}
	if _, err := filterCondition("mysql", "int", "`n`", TableFilter{"n", filterContains, "1"}); err == nil {
		t.Error("contains on a numeric column must be refused")
	}
	if _, err := filterCondition("postgresql", "int", `"n"`, TableFilter{"n", filterLT, ""}); err == nil {
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
