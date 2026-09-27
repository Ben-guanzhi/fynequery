package main

import "testing"

// TestCatalogDialectsComplete guards the op-and-type table: every known engine
// tag must have a dialect row, and every row must implement every catalog
// operation. A missing field would only surface as a nil call at runtime.
func TestCatalogDialectsComplete(t *testing.T) {
	for _, typ := range []string{dbTypeMySQL, dbTypePostgres, dbTypeSQLite, dbTypeSQLServer, dbTypeOracle} {
		d, err := dialectFor(typ)
		if err != nil {
			t.Errorf("dialectFor(%s): %v", typ, err)
			continue
		}
		if d.databases == nil || d.tables == nil || d.columns == nil ||
			d.search == nil || d.foreignKeys == nil || d.fkValuesSQL == nil || d.qualifiedTable == nil ||
			d.indexes == nil || d.catalogSchema == nil || d.tableSchema == nil {
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
	if got := resolveSchema(dbTypeMySQL, "shop", ""); got != "shop" {
		t.Errorf("resolveSchema(mysql) = %q, want shop", got)
	}
	if got := resolveSchema(dbTypeSQLServer, "erp", ""); got != "dbo" {
		t.Errorf("resolveSchema(sqlserver) = %q, want dbo", got)
	}
	if got := defaultSchema(dbTypeMySQL, ""); got != "" {
		t.Errorf("defaultSchema(mysql) = %q, want empty (db.table needs no schema)", got)
	}
	if got := defaultSchema(dbTypeSQLServer, ""); got != "dbo" {
		t.Errorf("defaultSchema(sqlserver) = %q, want dbo", got)
	}
}

// TestQualifiedTableDispatch checks that the generic tableName operation picks
// each engine's own addressing scheme.
func TestQualifiedTableDispatch(t *testing.T) {
	cases := []struct {
		dbType           string
		database, schema string
		want             string
	}{
		{dbTypeMySQL, "shop", "", "`shop`.`t`"},
		{dbTypeMySQL, "shop", "ignored", "`shop`.`t`"}, // mysql addresses db.table
		{dbTypePostgres, "shop", "public", `"public"."t"`},
		{dbTypeSQLServer, "erp", "sales", "[erp].[sales].[t]"},
		{dbTypeSQLite, "", "", `"t"`},
		{dbTypeOracle, "", "", `"t"`},
	}
	for _, tc := range cases {
		got, err := tableName(tc.dbType, tc.database, tc.schema, "t")
		if err != nil {
			t.Errorf("tableName(%s): %v", tc.dbType, err)
			continue
		}
		if got != tc.want {
			t.Errorf("tableName(%s) = %s, want %s", tc.dbType, got, tc.want)
		}
	}
}
