package db

import (
	"fmt"
	"strings"
)

// Ported from the parent TableProGo project: the CREATE TABLE renderer for
// the structure page. It reconstructs the DDL from catalog rows — a reading
// aid rather than a migration tool, so engine-level storage clauses are left
// out.

func ddlIdent(dbType, part string) string {
	quoted, err := quoteIdent(dbType, part)
	if err != nil {
		return part
	}
	return quoted
}

func ddlColumns(dbType string, names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, ddlIdent(dbType, name))
	}
	return strings.Join(quoted, ", ")
}

// ddlTableName qualifies only when a schema exists, because SQLite has no
// schema argument in the tree and "main" would be noise in the DDL.
func ddlTableName(dbType, schema, table string) string {
	if schema == "" || schema == "main" {
		return ddlIdent(dbType, table)
	}
	return ddlIdent(dbType, schema) + "." + ddlIdent(dbType, table)
}

// normalizeDefault unwraps the parentheses MSSQL wraps around every default
// and trims the padding Oracle adds to expression text.
func normalizeDefault(dbType, value string) string {
	if dbType == DbTypeSQLServer {
		for strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
			inner := strings.TrimSpace(value[1 : len(value)-1])
			if inner == "" {
				break
			}
			value = inner
		}
	}
	return value
}

// buildCreateTable renders CREATE TABLE plus the secondary indexes and
// foreign keys, from the same catalog rows the structure page shows.
func buildCreateTable(dbType, schema, table string, columns []ColumnInfo, types map[string]string, indexes []IndexInfo, foreignKeys []ForeignKeyInfo) string {
	object := ddlTableName(dbType, schema, table)

	definitions := make([]string, 0, len(columns)+1)
	pkColumns := make([]string, 0, 2)
	for _, column := range columns {
		dataType := column.Type
		if full, ok := types[column.Name]; ok && full != "" {
			dataType = full
		}
		line := "  " + ddlIdent(dbType, column.Name) + " " + dataType
		if value := strings.TrimSpace(column.DefaultValue); value != "" {
			line += " DEFAULT " + normalizeDefault(dbType, value)
		}
		if !column.Nullable {
			line += " NOT NULL"
		}
		if column.IsPrimaryKey {
			pkColumns = append(pkColumns, column.Name)
		}
		definitions = append(definitions, line)
	}

	hasPKConstraint := false
	for _, index := range indexes {
		if !index.Primary {
			continue
		}
		hasPKConstraint = true
		definitions = append(definitions,
			"  CONSTRAINT "+ddlIdent(dbType, index.Name)+" PRIMARY KEY ("+ddlColumns(dbType, index.Columns)+")")
	}
	// A table whose key the catalog hides (SQLite skips its autoindexes)
	// still deserves its key in the reading aid.
	if !hasPKConstraint && len(pkColumns) > 0 {
		definitions = append(definitions, "  PRIMARY KEY ("+ddlColumns(dbType, pkColumns)+")")
	}

	statements := []string{}
	statements = append(statements, "CREATE TABLE "+object+" (\n"+strings.Join(definitions, ",\n")+"\n);")

	for _, index := range indexes {
		if index.Primary || len(index.Columns) == 0 {
			continue
		}
		unique := ""
		if index.Unique {
			unique = "UNIQUE "
		}
		statements = append(statements, fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);",
			unique, ddlIdent(dbType, index.Name), object, ddlColumns(dbType, index.Columns)))
	}

	for _, fk := range foreignKeys {
		referenced := ddlTableName(dbType, fk.RefSchema, fk.RefTable)
		line := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)",
			object, ddlIdent(dbType, fk.Name), ddlColumns(dbType, fk.Columns), referenced, ddlColumns(dbType, fk.RefColumns))
		if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
			line += " ON DELETE " + fk.OnDelete
		}
		if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
			line += " ON UPDATE " + fk.OnUpdate
		}
		statements = append(statements, line+";")
	}

	return strings.Join(statements, "\n\n")
}

// tableDDL renders the DDL for one table from its catalog rows.
func (c *Store) TableDDL(connID, database, schema, table string) (string, error) {
	conn, err := c.OpenConn(connID)
	if err != nil {
		return "", err
	}
	if schema == "" {
		schema = ResolveSchema(conn.Config.Type, database, schema)
	}
	cols, err := c.Columns(connID, database, schema, table)
	if err != nil {
		return "", err
	}
	// Indexes and foreign keys are conveniences here: a catalog that refuses
	// them must not cost the user the column list.
	indexes, err := c.TableIndexes(connID, database, schema, table)
	if err != nil {
		indexes = nil
	}
	fks, err := c.ForeignKeys(connID, database, schema, table)
	if err != nil {
		fks = nil
	}
	return buildCreateTable(conn.Config.Type, schema, table, cols, nil, indexes, fks), nil
}

// ---- the "新建表" starter statement ----

// starterTable joins a template's lines. The templates are literal text on
// purpose: their whole job is to be read, edited and run by a person, so the
// only thing computed is the object name.
func starterTable(lines ...string) string {
	return strings.Join(lines, "\n")
}

// mysqlCreateTable: MySQL addresses tables by database, which the session
// already carries, so the template names the table bare.
func mysqlCreateTable(string) string {
	return starterTable(
		"CREATE TABLE `new_table` (",
		"  `id` INT NOT NULL AUTO_INCREMENT,",
		"  `name` VARCHAR(255) NULL,",
		"  `created_at` DATETIME NULL,",
		"  PRIMARY KEY (`id`)",
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;",
	)
}

// postgresCreateTable and sqlserverCreateTable write the schema they are given:
// CreateTableTemplate has already filled the engine's default namespace, because
// a dialect row asking its own table during initialization is a cycle.
func postgresCreateTable(schema string) string {
	return starterTable(
		"CREATE TABLE "+ddlIdent(DbTypePostgres, schema)+`."new_table" (`,
		"  id BIGSERIAL PRIMARY KEY,",
		"  name TEXT,",
		"  created_at TIMESTAMPTZ",
		");",
	)
}

func sqlserverCreateTable(schema string) string {
	return starterTable(
		"CREATE TABLE "+ddlIdent(DbTypeSQLServer, schema)+".[new_table] (",
		"  [id] INT IDENTITY(1,1) NOT NULL PRIMARY KEY,",
		"  [name] NVARCHAR(255) NULL,",
		"  [created_at] DATETIME2 NULL",
		");",
	)
}

// oracleCreateTable keeps Oracle's upper-case identifier convention, and the
// identity column that makes a bare row insertable.
func oracleCreateTable(string) string {
	return starterTable(
		`CREATE TABLE "NEW_TABLE" (`,
		`  "ID" NUMBER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,`,
		`  "NAME" VARCHAR2(255),`,
		`  "CREATED_AT" TIMESTAMP`,
		");",
	)
}

func sqliteCreateTable(string) string {
	return starterTable(
		`CREATE TABLE "new_table" (`,
		`  "id" INTEGER PRIMARY KEY AUTOINCREMENT,`,
		`  "name" TEXT,`,
		`  "created_at" TEXT`,
		");",
	)
}

// CreateTableTemplate answers the starter CREATE TABLE for one engine, so the
// navigator's "新建表" edits a name instead of recalling identity syntax. The
// schema is the navigator node's, defaulted to the engine's own namespace.
func CreateTableTemplate(dbType, schema string) (string, error) {
	d, err := dialectFor(dbType)
	if err != nil {
		return "", err
	}
	return d.createTable(DefaultSchema(dbType, schema)), nil
}
