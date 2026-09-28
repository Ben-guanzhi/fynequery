package db

import "testing"

// TestCatalogDialectsComplete guards the op-and-type table: every known engine
// tag must have a dialect row, and every row must implement every catalog
// operation. A missing field would only surface as a nil call at runtime.
func TestCatalogDialectsComplete(t *testing.T) {
	for _, typ := range []string{DbTypeMySQL, DbTypePostgres, DbTypeSQLite, DbTypeSQLServer, DbTypeOracle} {
		d, err := dialectFor(typ)
		if err != nil {
			t.Errorf("dialectFor(%s): %v", typ, err)
			continue
		}
		if d.databases == nil || d.tables == nil || d.columns == nil ||
			d.search == nil || d.foreignKeys == nil || d.fkValuesSQL == nil || d.qualifiedTable == nil ||
			d.indexes == nil || d.catalogSchema == nil || d.tableSchema == nil || d.pageSQL == nil ||
			d.createTable == nil {
			t.Errorf("dialect %s is missing catalog operations", typ)
		}
	}
	if _, err := dialectFor("nosuchengine"); err == nil {
		t.Error("an unknown engine must be refused")
	}
}

// TestSchemaNamespaceDispatch pins the two distinct namespace operations: a
// catalog lookup inside a database falls back to the database itself on MySQL,
// while a qualified table name never does.
func TestSchemaNamespaceDispatch(t *testing.T) {
	if got := ResolveSchema(DbTypeMySQL, "shop", ""); got != "shop" {
		t.Errorf("ResolveSchema(mysql) = %q, want shop", got)
	}
	if got := ResolveSchema(DbTypeSQLServer, "erp", ""); got != "dbo" {
		t.Errorf("ResolveSchema(sqlserver) = %q, want dbo", got)
	}
	if got := DefaultSchema(DbTypeMySQL, ""); got != "" {
		t.Errorf("DefaultSchema(mysql) = %q, want empty (db.table needs no schema)", got)
	}
	if got := DefaultSchema(DbTypeSQLServer, ""); got != "dbo" {
		t.Errorf("DefaultSchema(sqlserver) = %q, want dbo", got)
	}
}

// TestQualifiedTableDispatch checks that the generic TableName operation picks
// each engine's own addressing scheme.
func TestQualifiedTableDispatch(t *testing.T) {
	cases := []struct {
		dbType           string
		database, schema string
		want             string
	}{
		{DbTypeMySQL, "shop", "", "`shop`.`t`"},
		{DbTypeMySQL, "shop", "ignored", "`shop`.`t`"}, // mysql addresses db.table
		{DbTypePostgres, "shop", "public", `"public"."t"`},
		{DbTypeSQLServer, "erp", "sales", "[erp].[sales].[t]"},
		{DbTypeSQLite, "", "", `"t"`},
		{DbTypeOracle, "", "", `"t"`},
	}
	for _, tc := range cases {
		got, err := TableName(tc.dbType, tc.database, tc.schema, "t")
		if err != nil {
			t.Errorf("TableName(%s): %v", tc.dbType, err)
			continue
		}
		if got != tc.want {
			t.Errorf("TableName(%s) = %s, want %s", tc.dbType, got, tc.want)
		}
	}
}
