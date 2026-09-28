package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateTableTemplateByDialect pins the "新建表" starter statements: each
// engine gets its own identity syntax and its own way of naming the table, and
// every one of them must be a single complete statement, since it is handed to
// an editor the user runs as written.
func TestCreateTableTemplateByDialect(t *testing.T) {
	cases := []struct {
		dbType, schema, wantFirstLine string
	}{
		{DbTypeMySQL, "", "CREATE TABLE `new_table` ("},
		{DbTypePostgres, "", `CREATE TABLE "public"."new_table" (`},
		{DbTypePostgres, "report", `CREATE TABLE "report"."new_table" (`},
		{DbTypeSQLServer, "", "CREATE TABLE [dbo].[new_table] ("},
		{DbTypeSQLServer, "sales", "CREATE TABLE [sales].[new_table] ("},
		{DbTypeOracle, "", `CREATE TABLE "NEW_TABLE" (`},
		{DbTypeSQLite, "", `CREATE TABLE "new_table" (`},
	}
	for _, tc := range cases {
		stmt, err := CreateTableTemplate(tc.dbType, tc.schema)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.dbType, tc.schema, err)
		}
		if got := strings.SplitN(stmt, "\n", 2)[0]; got != tc.wantFirstLine {
			t.Errorf("%s/%s first line = %q, want %q", tc.dbType, tc.schema, got, tc.wantFirstLine)
		}
		parts := SplitStatements(stmt)
		if len(parts) != 1 {
			t.Errorf("%s: expected one statement, SplitStatements gave %d: %q", tc.dbType, len(parts), stmt)
		}
		if !strings.Contains(strings.ToUpper(stmt), "NEW_TABLE") {
			t.Errorf("%s: the template must name a table the user can rename, got %q", tc.dbType, stmt)
		}
	}
	if _, err := CreateTableTemplate("nosuchengine", ""); err == nil {
		t.Error("an unknown engine must be refused")
	}
}

// TestSQLiteCreateTableTemplateRuns proves the starter is not just plausible
// text: the SQLite one is executed against a real file and reads back as a
// table whose key is the identity column.
func TestSQLiteCreateTableTemplateRuns(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "starter.db")

	c := &Store{live: make(map[string]*Conn)}
	if err := c.Connect(ConnConfig{ID: "c0", Name: "t", Type: DbTypeSQLite, Database: dbFile}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect("c0") })

	stmt, err := CreateTableTemplate(DbTypeSQLite, "")
	if err != nil {
		t.Fatalf("CreateTableTemplate: %v", err)
	}
	conn, err := c.OpenConn("c0")
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	if _, err := conn.DB.Exec(stmt); err != nil {
		t.Fatalf("the starter statement must run: %v\n%s", err, stmt)
	}
	// An insert that names no id is what AUTOINCREMENT is for.
	if _, err := conn.DB.Exec(`INSERT INTO new_table (name) VALUES ('first')`); err != nil {
		t.Fatalf("identity column: %v", err)
	}

	cols, err := c.Columns("c0", dbFile, "", "new_table")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("expected 3 columns, got %+v", cols)
	}
	if !cols[0].IsPrimaryKey || cols[0].Name != "id" {
		t.Fatalf("expected id to be the primary key, got %+v", cols[0])
	}
}
