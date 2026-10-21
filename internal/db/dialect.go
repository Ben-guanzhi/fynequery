package db

import (
	"tablepro-fyne/internal/drivercore"

	"github.com/Ben-guanzhi/TableProGo/tableprosql"

	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Catalog dispatch, in the data-directed style: the engine identifier is the
// type tag, catalogDialects is the op-and-type table, and the Client-level
// readers (databases/schemas/tables/columns/...) are apply-generic. Adding an
// engine means appending one dialect row below, not editing every reader.
// The statements are moved verbatim from the switches they replace.

// dialect bundles one engine's catalog operations. Every field must be set
// (guarded by TestCatalogDialectsComplete); operations a dialect does not
// have return their engine's "nothing" convention directly.
//
// The operations that reach the server take the caller's context first: a
// catalog read is a query like any other, and one that cannot be interrupted is
// how a remote server froze the whole window.
type dialect struct {
	// databases lists the database names visible on the connection.
	databases func(ctx context.Context, c *Store, conn *Conn) ([]string, error)
	// schemas lists schema names inside one database; returning nil means the
	// engine has no schema layer and the caller goes straight to tables.
	schemas func(ctx context.Context, c *Store, conn *Conn, database string) ([]string, error)
	// tables lists tables and views of one database/schema pair.
	tables func(ctx context.Context, c *Store, conn *Conn, database, schema string) ([]TableInfo, error)
	// completionObjects names the catalog objects a SQL completion list should
	// offer beyond tables and views; nil means the engine has no such list.
	completionObjects func(ctx context.Context, c *Store, conn *Conn, database string) ([]string, error)
	// columns reads one table's column metadata.
	columns func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ColumnInfo, error)
	// search matches table/view names by fragment inside one database.
	search func(ctx context.Context, c *Store, conn *Conn, database, fragment string, limit int) ([]ObjectMatch, error)
	// foreignKeys reads one table's foreign-key constraints.
	foreignKeys func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ForeignKeyInfo, error)
	// fkValuesSQL renders the DISTINCT-keys query over a referenced table.
	fkValuesSQL func(name, refColumn, labelColumn string, limit int) (string, error)
	// qualifiedTable renders database.schema.table the way the engine
	// addresses tables.
	qualifiedTable func(database, schema, table string) (string, error)
	// indexes reads one table's index list.
	indexes func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]IndexInfo, error)
	// catalogSchema names the namespace catalog queries run against inside one
	// database. Engines without a schema layer answer with the database
	// itself; callers only ask while the schema is still empty.
	catalogSchema func(database, schema string) string
	// tableSchema fills the engine's default table namespace for qualified
	// names, independent of which database the session is in.
	tableSchema func(schema string) string
	// pageSQL renders a row window (offset rows skipped, then size rows) around
	// a statement the caller has already checked is pageable.
	pageSQL func(query string, offset, size int) string
	// createTable renders the starter CREATE TABLE for "新建表": the smallest
	// table this engine accepts, so the user edits a name and a column instead
	// of recalling each dialect's identity syntax. It takes the node's schema
	// and fills the engine's own default namespace.
	createTable func(schema string) string
	// insertRows renders one INSERT carrying several rows of already-rendered
	// literals: a CSV import is thousands of rows, and sending one statement
	// per row is thousands of round trips inside one transaction. The columns
	// arrive quoted for this engine and the values arrive as literals, so no
	// dialect has to know how the other is escaped.
	insertRows func(table string, columns []string, rows [][]string) string
	// maxInsertRows is how many rows one such statement may carry. It is an
	// engine fact, not a client policy: SQL Server caps a multi-row VALUES at
	// 1000, SQLite compiles one through a compound select with a default limit
	// of 500, and Oracle's multi-table INSERT is the one form here no server of
	// ours can verify, so it gets the most conservative batch of all.
	maxInsertRows int
}

func dialectFor(dbType string) (*dialect, error) {
	d, ok := catalogDialects[dbType]
	if !ok {
		return nil, fmt.Errorf("unsupported type: %s", dbType)
	}
	return d, nil
}

// IndexInfo describes one index; multi-column indexes keep their columns in
// catalog order.
type IndexInfo struct {
	Name    string
	Kind    string
	Unique  bool
	Primary bool
	Columns []string
}

// indexRow is the flat, one-row-per-column shape the catalogs return; the
// aggregated list is what the UI wants.
type indexRow struct {
	name    string
	kind    string
	column  string
	unique  bool
	primary bool
}

var catalogDialects = map[string]*dialect{
	DbTypeMySQL:     mysqlDialect,
	DbTypePostgres:  postgresDialect,
	DbTypeSQLite:    sqliteDialect,
	DbTypeSQLServer: sqlserverDialect,
	DbTypeOracle:    oracleDialect,
}

// ---- shared scan helpers ----

// collectRows runs query on the connection's pool and folds every row through
// scan: the higher-order loop every catalog reader shares - open, scan into a T,
// close, report the cursor's own error.
//
// The handle comes from Conn.mu, which is what keeps a reader that races a
// Disconnect honest: it either gets the not-connected error or runs on a handle
// that is merely closed, and the Engine answers that with an error too. Read
// straight off an exported field the same reader could dereference a handle
// Disconnect had just nilled.
//
// The scanner takes a drivercore.RowSet rather than *sql.Rows: an in-process
// engine hands back the real *sql.Rows, a driver plugin a materialized adapter,
// and both satisfy RowSet, so no reader branches on which backend answered.
func collectRows[T any](ctx context.Context, conn *Conn, query string, args []interface{}, scan func(drivercore.RowSet) (T, error)) ([]T, error) {
	engine, err := conn.handle()
	if err != nil {
		return nil, err
	}
	return collectRowsOn(ctx, engine, query, args, scan)
}

// collectRowsOn is collectRows for a pool the caller has already chosen: the
// per-database sessions poolFor hands out.
func collectRowsOn[T any](ctx context.Context, engine drivercore.Engine, query string, args []interface{}, scan func(drivercore.RowSet) (T, error)) ([]T, error) {
	rows, err := engine.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// scanCrossSchemaTable reads (schema, name, type) rows for the unfiltered
// table listing used when a schema filter matches nothing.
func scanCrossSchemaTable(r drivercore.RowSet) (TableInfo, error) {
	var ts, name, typ string
	if err := r.Scan(&ts, &name, &typ); err != nil {
		return TableInfo{}, err
	}
	return TableInfo{Name: name, Type: normalizeTableType(typ), Schema: ts}, nil
}

// scanString reads the one column such queries select.
func scanString(r drivercore.RowSet) (string, error) {
	var s string
	return s, r.Scan(&s)
}

// tableScanner pins every hit to the schema it was asked about.
func tableScanner(schema string) func(drivercore.RowSet) (TableInfo, error) {
	return func(r drivercore.RowSet) (TableInfo, error) {
		var t TableInfo
		if err := r.Scan(&t.Name, &t.Type, &t.Comment); err != nil {
			return t, err
		}
		t.Type = normalizeTableType(t.Type)
		t.Schema = schema
		return t, nil
	}
}

// matchScanner stamps the connection and database a hit came from.
func matchScanner(conn *Conn, database string) func(drivercore.RowSet) (ObjectMatch, error) {
	return func(r drivercore.RowSet) (ObjectMatch, error) {
		var m ObjectMatch
		var objectType string
		if err := r.Scan(&m.Schema, &m.Table, &objectType); err != nil {
			return m, err
		}
		m.ConnID = conn.Config.ID
		m.Database = database
		m.Type = normalizeTableType(objectType)
		return m, nil
	}
}

func scanStandardColumn(r drivercore.RowSet) (ColumnInfo, error) {
	var col ColumnInfo
	var defaultValue, comment sql.NullString
	var nullable string
	var isPK int
	if err := r.Scan(&col.Name, &col.Type, &nullable, &defaultValue, &isPK, &comment); err != nil {
		return col, err
	}
	col.Nullable = nullable == "YES" || nullable == "1" || nullable == "Y"
	col.IsPrimaryKey = isPK == 1
	if defaultValue.Valid {
		col.DefaultValue = defaultValue.String
	}
	if comment.Valid {
		col.Comment = comment.String
	}
	return col, nil
}

func scanSQLiteColumn(r drivercore.RowSet) (ColumnInfo, error) {
	var col ColumnInfo
	var cid, notnull, pk int
	var defaultValue sql.NullString
	if err := r.Scan(&cid, &col.Name, &col.Type, &notnull, &defaultValue, &pk); err != nil {
		return col, err
	}
	col.Nullable = notnull == 0
	// PRAGMA table_info numbers the primary-key columns 1..n and uses 0 for the
	// rest, so a composite key must test for any non-zero position. Testing
	// ==1 silently kept only the first column, which made a composite key's
	// write-back WHERE clause under-constrain the row it updates.
	col.IsPrimaryKey = pk > 0
	if defaultValue.Valid {
		col.DefaultValue = defaultValue.String
	}
	return col, nil
}

func scanFkRow(r drivercore.RowSet) (tableprosql.FKRow, error) {
	var row tableprosql.FKRow
	err := r.Scan(&row.Name, &row.Column, &row.RefSchema, &row.RefTable, &row.RefColumn,
		&row.OnDelete, &row.OnUpdate)
	return row, err
}

// scanIndexRow reads one flat catalog row: name, kind, unique, primary, column.
// Every engine's query has to answer for all five - the arity is checked by the
// row itself at scan time, so a short select list fails the read on a real
// server and nowhere else.
func scanIndexRow(r drivercore.RowSet) (indexRow, error) {
	var row indexRow
	var unique, primary int
	if err := r.Scan(&row.name, &row.kind, &unique, &primary, &row.column); err != nil {
		return row, err
	}
	row.unique, row.primary = unique == 1, primary == 1
	return row, nil
}

// aggregateIndexes folds the flat one-row-per-column catalog shape into one
// IndexInfo per index, columns in catalog order.
func aggregateIndexes(rows []indexRow) []IndexInfo {
	indexes := []IndexInfo{}
	byName := map[string]int{}
	for _, row := range rows {
		i, ok := byName[row.name]
		if !ok {
			byName[row.name] = len(indexes)
			indexes = append(indexes, IndexInfo{
				Name:    row.name,
				Kind:    row.kind,
				Unique:  row.unique,
				Primary: row.primary,
			})
			i = len(indexes) - 1
		}
		indexes[i].Columns = append(indexes[i].Columns, row.column)
	}
	return indexes
}

// ---- shared INSERT renderers ----

// multiValuesInsert is the ANSI multi-row INSERT: INSERT INTO t (a, b) VALUES
// (1, 2), (3, 4). The literals are already rendered for the engine, so this
// only has to interleave punctuation.
func multiValuesInsert(table string, columns []string, rows [][]string) string {
	var sb strings.Builder
	sb.WriteString("INSERT INTO ")
	sb.WriteString(table)
	sb.WriteString(" (")
	sb.WriteString(strings.Join(columns, ", "))
	sb.WriteString(") VALUES ")
	for i, row := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(")
		sb.WriteString(strings.Join(row, ", "))
		sb.WriteString(")")
	}
	return sb.String()
}

// insertAllInsert is Oracle's multi-table INSERT, which is the only way it
// takes more than one row of literal values: a plain VALUES clause there holds
// exactly one row.
func insertAllInsert(table string, columns []string, rows [][]string) string {
	cols := strings.Join(columns, ", ")
	var sb strings.Builder
	sb.WriteString("INSERT ALL ")
	for _, row := range rows {
		sb.WriteString("INTO ")
		sb.WriteString(table)
		sb.WriteString(" (")
		sb.WriteString(cols)
		sb.WriteString(") VALUES (")
		sb.WriteString(strings.Join(row, ", "))
		sb.WriteString(") ")
	}
	sb.WriteString("SELECT 1 FROM dual")
	return sb.String()
}

// ---- per-engine dialects ----

var mysqlDialect = &dialect{
	databases: func(ctx context.Context, c *Store, conn *Conn) ([]string, error) {
		return collectRows(ctx, conn, "SHOW DATABASES", nil, scanString)
	},
	schemas: nil, // the database is the schema
	tables: func(ctx context.Context, c *Store, conn *Conn, database, schema string) ([]TableInfo, error) {
		return collectRows(ctx, conn, `SELECT TABLE_NAME, TABLE_TYPE, TABLE_COMMENT FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`,
			[]interface{}{database}, tableScanner(schema))
	},
	columns: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ColumnInfo, error) {
		return collectRows(ctx, conn, `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT,
			CASE WHEN COLUMN_KEY='PRI' THEN 1 ELSE 0 END, COLUMN_COMMENT
			FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=?
			ORDER BY ORDINAL_POSITION`,
			[]interface{}{schema, table}, scanStandardColumn)
	},
	search: func(ctx context.Context, c *Store, conn *Conn, database, fragment string, limit int) ([]ObjectMatch, error) {
		query := fmt.Sprintf(`SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME LIKE ? ORDER BY TABLE_NAME LIMIT %d`, limit)
		return collectRows(ctx, conn, query, []interface{}{database, likePattern(fragment)},
			matchScanner(conn, database))
	},
	foreignKeys: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ForeignKeyInfo, error) {
		rows, err := collectRows(ctx, conn, `SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA,
			k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
			IFNULL(r.DELETE_RULE, ''), IFNULL(r.UPDATE_RULE, '')
			FROM information_schema.KEY_COLUMN_USAGE k
			LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS r
				ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
			WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
			ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`,
			[]interface{}{schema, table}, scanFkRow)
		if err != nil {
			return nil, err
		}
		return aggregateForeignKeys(rows), nil
	},
	fkValuesSQL: func(name, refColumn, labelColumn string, limit int) (string, error) {
		return fkValuesQueryLimit(DbTypeMySQL, name, refColumn, labelColumn, limit, "LIMIT %d")
	},
	qualifiedTable: func(database, schema, table string) (string, error) {
		return QualifiedName(DbTypeMySQL, database, table)
	},
	indexes: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]IndexInfo, error) {
		// The same statement the root client sends, so one engine has one index
		// catalog SQL. Both flags come back derived: STATISTICS has no is-primary
		// column (MySQL marks the key by naming it PRIMARY) and NON_UNIQUE holds
		// the opposite of what scanIndexRow scans into. The MySQL row of the index
		// catalog contract catches either mistake - whenever a server is configured.
		query, args, err := tableprosql.IndexesQuery(DbTypeMySQL, database, schema, table)
		if err != nil {
			return nil, err
		}
		flat, err := collectRows(ctx, conn, query, args, scanIndexRow)
		if err != nil {
			return nil, err
		}
		return aggregateIndexes(flat), nil
	},
	catalogSchema: func(database, schema string) string {
		return database // the database is the schema
	},
	tableSchema: func(schema string) string {
		return schema
	},
	pageSQL:     limitOffset,
	createTable: mysqlCreateTable,
	// A packet cannot exceed max_allowed_packet, so a batch stays well under it
	// rather than measuring the rendered statement.
	insertRows:    multiValuesInsert,
	maxInsertRows: 1000,
}

var postgresDialect = &dialect{
	databases: func(ctx context.Context, c *Store, conn *Conn) ([]string, error) {
		return collectRows(ctx, conn, "SELECT datname FROM pg_database WHERE datistemplate = false", nil, scanString)
	},
	schemas: func(ctx context.Context, c *Store, conn *Conn, database string) ([]string, error) {
		return scanStrings(ctx, conn, `SELECT nspname FROM pg_namespace
			WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
			ORDER BY nspname`)
	},
	tables: func(ctx context.Context, c *Store, conn *Conn, database, schema string) ([]TableInfo, error) {
		db, err := c.poolFor(ctx, conn, database)
		if err != nil {
			return nil, err
		}
		tables, err := collectRowsOn(ctx, db, `SELECT tablename, 'BASE TABLE', '' FROM pg_tables WHERE schemaname = $1
			UNION ALL
			SELECT viewname, 'VIEW', '' FROM pg_views WHERE schemaname = $1
			ORDER BY 1`,
			[]interface{}{schema}, tableScanner(schema))
		if err != nil || len(tables) > 0 {
			return tables, err
		}
		fallback := `SELECT n.nspname, c.relname, CASE WHEN c.relkind = 'v' THEN 'VIEW' ELSE 'TABLE' END
			FROM pg_class c
			INNER JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN ('r', 'v', 'm', 'p')
				AND n.nspname NOT IN ('pg_catalog', 'information_schema')
			ORDER BY 1, 2`
		return collectRowsOn(ctx, db, fallback, nil, scanCrossSchemaTable)
	},
	columns: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ColumnInfo, error) {
		db, err := c.poolFor(ctx, conn, database)
		if err != nil {
			return nil, err
		}
		return collectRowsOn(ctx, db, `SELECT a.attname, format_type(a.atttypid, a.atttypmod),
			CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END,
			COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
			CASE WHEN i.indisprimary THEN 1 ELSE 0 END,
			COALESCE(col_description(c.oid, a.attnum), '')
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
			LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
			LEFT JOIN pg_index i ON i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY(i.indkey)
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','v','m','p')
			ORDER BY a.attnum`,
			[]interface{}{schema, table}, scanStandardColumn)
	},
	search: func(ctx context.Context, c *Store, conn *Conn, database, fragment string, limit int) ([]ObjectMatch, error) {
		db, err := c.poolFor(ctx, conn, database)
		if err != nil {
			return nil, err
		}
		query := fmt.Sprintf(`SELECT n.nspname, c.relname, CASE WHEN c.relkind = 'v' THEN 'VIEW' ELSE 'TABLE' END
			FROM pg_class c
			INNER JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN ('r', 'v', 'm', 'p')
				AND n.nspname NOT IN ('pg_catalog', 'information_schema')
				AND c.relname ILIKE $1
			ORDER BY 1, 2 LIMIT %d`, limit)
		return collectRowsOn(ctx, db, query, []interface{}{likePattern(fragment)}, matchScanner(conn, database))
	},
	foreignKeys: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ForeignKeyInfo, error) {
		db, err := c.poolFor(ctx, conn, database)
		if err != nil {
			return nil, err
		}
		rows, err := collectRowsOn(ctx, db, `SELECT con.conname, xg.attname, confnsp.nspname, confcls.relname, yg.attname,
			COALESCE(CASE con.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
				WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END, ''),
			COALESCE(CASE con.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
				WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END, '')
			FROM pg_constraint con
			JOIN pg_class cls ON cls.oid = con.conrelid
			JOIN pg_namespace nsp ON nsp.oid = cls.relnamespace
			JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS x(attnum, ord) ON true
			JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS y(attnum, ord) ON y.ord = x.ord
			JOIN pg_attribute xg ON xg.attrelid = con.conrelid AND xg.attnum = x.attnum
			JOIN pg_attribute yg ON yg.attrelid = con.confrelid AND yg.attnum = y.attnum
			JOIN pg_class confcls ON confcls.oid = con.confrelid
			JOIN pg_namespace confnsp ON confnsp.oid = confcls.relnamespace
			WHERE con.contype = 'f' AND nsp.nspname = $1 AND cls.relname = $2
			ORDER BY con.conname, x.ord`,
			[]interface{}{schema, table}, scanFkRow)
		if err != nil {
			return nil, err
		}
		return aggregateForeignKeys(rows), nil
	},
	fkValuesSQL: func(name, refColumn, labelColumn string, limit int) (string, error) {
		return fkValuesQueryLimit(DbTypePostgres, name, refColumn, labelColumn, limit, "LIMIT %d")
	},
	qualifiedTable: func(database, schema, table string) (string, error) {
		return QualifiedName(DbTypePostgres, schema, table)
	},
	indexes: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]IndexInfo, error) {
		db, err := c.poolFor(ctx, conn, database)
		if err != nil {
			return nil, err
		}
		flat, err := collectRowsOn(ctx, db, `SELECT c.relname, am.amname, CASE WHEN i.indisunique THEN 1 ELSE 0 END,
			CASE WHEN i.indisprimary THEN 1 ELSE 0 END, x.attname
			FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_am am ON am.oid = c.relam
			JOIN pg_class t ON t.oid = i.indrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
			JOIN pg_attribute x ON x.attrelid = i.indrelid AND x.attnum = k.attnum
			WHERE n.nspname = $1 AND t.relname = $2
			ORDER BY c.relname, k.ord`,
			[]interface{}{schema, table}, scanIndexRow)
		if err != nil {
			return nil, err
		}
		return aggregateIndexes(flat), nil
	},
	catalogSchema: func(database, schema string) string {
		return "public"
	},
	tableSchema: func(schema string) string {
		if schema != "" {
			return schema
		}
		return "public"
	},
	pageSQL:     limitOffset,
	createTable: postgresCreateTable,
	// No documented cap on the number of VALUES tuples; the batch matches
	// MySQL's so a test that counts batches does not need per-engine numbers.
	insertRows:    multiValuesInsert,
	maxInsertRows: 1000,
}

var sqliteDialect = &dialect{
	databases: func(ctx context.Context, c *Store, conn *Conn) ([]string, error) {
		return []string{conn.Config.Database}, nil
	},
	schemas: nil, // one main schema
	tables: func(ctx context.Context, c *Store, conn *Conn, database, schema string) ([]TableInfo, error) {
		return collectRows(ctx, conn, `SELECT name, type, '' FROM sqlite_master
			WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`,
			nil, tableScanner(schema))
	},
	columns: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ColumnInfo, error) {
		name, err := quoteIdent(DbTypeSQLite, table)
		if err != nil {
			return nil, err
		}
		return collectRows(ctx, conn, fmt.Sprintf("PRAGMA table_info(%s)", name), nil, scanSQLiteColumn)
	},
	search: func(ctx context.Context, c *Store, conn *Conn, database, fragment string, limit int) ([]ObjectMatch, error) {
		// The literal 'sqlite_%' needs its percent doubled: this whole string
		// is a Sprintf format, and a bare % would corrupt the query. Unlike
		// the other engines SQLite has no default LIKE escape, so one is
		// declared.
		query := fmt.Sprintf(`SELECT '', name, UPPER(type) FROM sqlite_master
			WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%%' AND name LIKE ? ESCAPE '\'
			ORDER BY name LIMIT %d`, limit)
		return collectRows(ctx, conn, query, []interface{}{likePattern(fragment)},
			matchScanner(conn, database))
	},
	foreignKeys: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ForeignKeyInfo, error) {
		name, err := quoteIdent(DbTypeSQLite, table)
		if err != nil {
			return nil, err
		}
		rows, err := collectRows(ctx, conn, fmt.Sprintf("PRAGMA foreign_key_list(%s)", name), nil,
			func(r drivercore.RowSet) (tableprosql.FKRow, error) {
				var id, seq int
				var row tableprosql.FKRow
				var onUpdate, onDelete, match string
				if err := r.Scan(&id, &seq, &row.RefTable, &row.Column, &row.RefColumn,
					&onUpdate, &onDelete, &match); err != nil {
					return row, err
				}
				row.Name = fmt.Sprintf("fk_%d", id)
				row.OnUpdate, row.OnDelete = onUpdate, onDelete
				return row, nil
			})
		if err != nil {
			return nil, err
		}
		return aggregateForeignKeys(rows), nil
	},
	fkValuesSQL: func(name, refColumn, labelColumn string, limit int) (string, error) {
		return fkValuesQueryLimit(DbTypeSQLite, name, refColumn, labelColumn, limit, "LIMIT %d")
	},
	qualifiedTable: func(database, schema, table string) (string, error) {
		return QualifiedName(DbTypeSQLite, table)
	},
	indexes: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]IndexInfo, error) {
		name, err := quoteIdent(DbTypeSQLite, table)
		if err != nil {
			return nil, err
		}
		type entry struct {
			name   string
			origin string
			unique bool
		}
		entries, err := collectRows(ctx, conn, fmt.Sprintf("PRAGMA index_list(%s)", name), nil,
			func(r drivercore.RowSet) (entry, error) {
				var seq int
				var e entry
				var unique int
				var partial string
				if err := r.Scan(&seq, &e.name, &unique, &e.origin, &partial); err != nil {
					return e, err
				}
				e.unique = unique == 1
				return e, nil
			})
		if err != nil {
			return nil, err
		}
		indexes := []IndexInfo{}
		for _, e := range entries {
			// Implicit indexes SQLite builds for UNIQUE/PRIMARY KEY constraints
			// are already covered by the constraint itself.
			if strings.HasPrefix(e.name, "sqlite_autoindex") {
				continue
			}
			idxName, err := quoteIdent(DbTypeSQLite, e.name)
			if err != nil {
				return nil, err
			}
			columns, err := collectRows(ctx, conn, fmt.Sprintf("PRAGMA index_info(%s)", idxName), nil,
				func(r drivercore.RowSet) (string, error) {
					var seqno, cid int
					var col string
					if err := r.Scan(&seqno, &cid, &col); err != nil {
						return "", err
					}
					return col, nil
				})
			if err != nil {
				return nil, err
			}
			indexes = append(indexes, IndexInfo{
				Name:    e.name,
				Kind:    e.origin,
				Unique:  e.unique,
				Primary: e.origin == "pk",
				Columns: columns,
			})
		}
		return indexes, nil
	},
	catalogSchema: func(database, schema string) string {
		return schema
	},
	tableSchema: func(schema string) string {
		return schema
	},
	pageSQL:     limitOffset,
	createTable: sqliteCreateTable,
	// A multi-row VALUES is compiled as a compound select, and
	// SQLITE_MAX_COMPOUND_SELECT defaults to 500 terms.
	insertRows:    multiValuesInsert,
	maxInsertRows: 500,
}

var sqlserverDialect = &dialect{
	databases: func(ctx context.Context, c *Store, conn *Conn) ([]string, error) {
		return collectRows(ctx, conn, "SELECT name FROM sys.databases ORDER BY name", nil, scanString)
	},
	schemas: func(ctx context.Context, c *Store, conn *Conn, database string) ([]string, error) {
		name, err := quoteIdent(DbTypeSQLServer, database)
		if err != nil {
			return nil, err
		}
		return scanStrings(ctx, conn, fmt.Sprintf(`SELECT s.name FROM %s.sys.schemas s
			WHERE s.name NOT LIKE 'db[_]%%' AND s.name NOT IN ('sys','INFORMATION_SCHEMA','guest')
			ORDER BY s.name`, name))
	},
	tables: func(ctx context.Context, c *Store, conn *Conn, database, schema string) ([]TableInfo, error) {
		name, err := quoteIdent(DbTypeSQLServer, database)
		if err != nil {
			return nil, err
		}
		query := fmt.Sprintf(`SELECT TABLE_NAME, TABLE_TYPE, '' FROM %s.INFORMATION_SCHEMA.TABLES
			WHERE TABLE_SCHEMA = %s ORDER BY TABLE_NAME`, name, sqlLiteral(schema))
		tables, err := collectRows(ctx, conn, query, nil, tableScanner(schema))
		if err != nil || len(tables) > 0 {
			return tables, err
		}
		// The navigator guessed the schema (dbo); a database whose tables live
		// under another schema would look empty. List them all, labeled with
		// their real one.
		fallback := fmt.Sprintf(`SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE FROM %s.INFORMATION_SCHEMA.TABLES ORDER BY TABLE_SCHEMA, TABLE_NAME`, name)
		return collectRows(ctx, conn, fallback, nil, scanCrossSchemaTable)
	},
	columns: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ColumnInfo, error) {
		name, err := quoteIdent(DbTypeSQLServer, database)
		if err != nil {
			return nil, err
		}
		query := fmt.Sprintf(`SELECT c.COLUMN_NAME, c.DATA_TYPE, c.IS_NULLABLE, c.COLUMN_DEFAULT,
			CASE WHEN pk.COLUMN_NAME IS NOT NULL THEN 1 ELSE 0 END, ''
			FROM %s.INFORMATION_SCHEMA.COLUMNS c
			LEFT JOIN (
				SELECT ku.TABLE_SCHEMA, ku.TABLE_NAME, ku.COLUMN_NAME
				FROM %s.INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
				INNER JOIN %s.INFORMATION_SCHEMA.KEY_COLUMN_USAGE ku
					ON ku.CONSTRAINT_NAME=tc.CONSTRAINT_NAME AND ku.TABLE_NAME=tc.TABLE_NAME
					AND ku.TABLE_SCHEMA=tc.TABLE_SCHEMA
				WHERE tc.CONSTRAINT_TYPE='PRIMARY KEY'
			) pk ON pk.TABLE_SCHEMA=c.TABLE_SCHEMA AND pk.TABLE_NAME=c.TABLE_NAME AND pk.COLUMN_NAME=c.COLUMN_NAME
			WHERE c.TABLE_NAME=%s AND c.TABLE_SCHEMA=%s ORDER BY c.ORDINAL_POSITION`,
			name, name, name, sqlLiteral(table), sqlLiteral(schema))
		return collectRows(ctx, conn, query, nil, scanStandardColumn)
	},
	search: func(ctx context.Context, c *Store, conn *Conn, database, fragment string, limit int) ([]ObjectMatch, error) {
		name, err := quoteIdent(DbTypeSQLServer, database)
		if err != nil {
			return nil, err
		}
		// go-mssqldb leaves "?" markers untouched in some statements, so the
		// pattern goes in as an escaped literal like the other catalog reads.
		query := fmt.Sprintf(`SELECT TOP (%d) TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE
			FROM %s.INFORMATION_SCHEMA.TABLES
			WHERE TABLE_NAME LIKE %s ESCAPE '\' ORDER BY TABLE_NAME`,
			limit, name, sqlLiteral(likePattern(fragment)))
		return collectRows(ctx, conn, query, nil, matchScanner(conn, database))
	},
	foreignKeys: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ForeignKeyInfo, error) {
		quoted, err := quoteIdent(DbTypeSQLServer, database)
		if err != nil {
			return nil, err
		}
		objectID, err := QualifiedName(DbTypeSQLServer, database, schema, table)
		if err != nil {
			return nil, err
		}
		literal := sqlLiteral(objectID)
		// Every name comes from a joined catalog row rather than
		// OBJECT_SCHEMA_NAME/COL_NAME, because those resolve against the
		// session's own database and this pool may sit in another one.
		query := fmt.Sprintf(`SELECT fk.name, col.name, rs.name, ref.name, refcol.name,
			fk.delete_referential_action_desc, fk.update_referential_action_desc
			FROM %s.sys.foreign_keys fk
			INNER JOIN %s.sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
			INNER JOIN %s.sys.columns col ON col.object_id = fkc.parent_object_id
				AND col.column_id = fkc.parent_column_id
			INNER JOIN %s.sys.tables ref ON ref.object_id = fk.referenced_object_id
			INNER JOIN %s.sys.schemas rs ON rs.schema_id = ref.schema_id
			INNER JOIN %s.sys.columns refcol ON refcol.object_id = fkc.referenced_object_id
				AND refcol.column_id = fkc.referenced_column_id
			WHERE fk.parent_object_id = OBJECT_ID(%s)
			ORDER BY fk.name, fkc.constraint_column_id`,
			quoted, quoted, quoted, quoted, quoted, quoted, literal)
		rows, err := collectRows(ctx, conn, query, nil, scanFkRow)
		if err != nil {
			return nil, err
		}
		return aggregateForeignKeys(rows), nil
	},
	fkValuesSQL: func(name, refColumn, labelColumn string, limit int) (string, error) {
		return fkValuesQueryLimit(DbTypeSQLServer, name, refColumn, labelColumn, limit,
			"OFFSET 0 ROWS FETCH NEXT %d ROWS ONLY")
	},
	qualifiedTable: func(database, schema, table string) (string, error) {
		return QualifiedName(DbTypeSQLServer, database, schema, table)
	},
	indexes: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]IndexInfo, error) {
		// Indexes are per-object, so the catalog of the target database is
		// reached through a qualified name instead of a separate connection.
		quoted, err := quoteIdent(DbTypeSQLServer, database)
		if err != nil {
			return nil, err
		}
		objectID, err := QualifiedName(DbTypeSQLServer, database, schema, table)
		if err != nil {
			return nil, err
		}
		literal := sqlLiteral(objectID)
		flat, err := collectRows(ctx, conn, fmt.Sprintf(`SELECT i.name, i.type_desc, CAST(i.is_unique AS int),
			CAST(i.is_primary_key AS int), col.name
			FROM %s.sys.indexes i
			INNER JOIN %s.sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
			INNER JOIN %s.sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
			WHERE i.object_id = OBJECT_ID(%s) AND i.is_hypothetical = 0
			ORDER BY i.name, ic.key_ordinal`, quoted, quoted, quoted, literal), nil, scanIndexRow)
		if err != nil {
			return nil, err
		}
		return aggregateIndexes(flat), nil
	},
	catalogSchema: func(database, schema string) string {
		return "dbo"
	},
	tableSchema: func(schema string) string {
		if schema != "" {
			return schema
		}
		return "dbo"
	},
	// SQL Server only accepts the window clause after an ORDER BY, so a
	// statement without its own sort gets a constant one.
	pageSQL: func(query string, offset, size int) string {
		if !outerOrderByRe.MatchString(query) {
			query += " ORDER BY (SELECT NULL)"
		}
		return offsetFetch(query, offset, size)
	},
	createTable: sqlserverCreateTable,
	// The documented cap is 1000 rows per multi-row VALUES. No SQL Server is
	// reachable from this repo's tests, so the batch sits at half the cap
	// instead of right up against it.
	insertRows:    multiValuesInsert,
	maxInsertRows: 500,
}

// oracleCompletionObjectsSQL is PL/SQL Developer's CANames.sql - the Code
// Assistant's name list - folded into one round trip. CANames.sql ships three
// statements and returns name+type pairs; completion needs only the name, and a
// UNION ALL answers in one query instead of three. Only the alias is returned
// for a synonym: the join keeps CANames' filter (a synonym is listed only when
// it resolves to one of the object types below), but the alias is what you
// type in SQL.
const oracleCompletionObjectsSQL = `SELECT OBJECT_NAME FROM ALL_OBJECTS
	WHERE OWNER = USER
		AND OBJECT_TYPE IN ('TABLE', 'VIEW', 'PACKAGE', 'TYPE', 'PROCEDURE', 'FUNCTION', 'SEQUENCE')
	UNION ALL
	SELECT s.SYNONYM_NAME FROM ALL_SYNONYMS s
	JOIN ALL_OBJECTS o ON o.OWNER = s.TABLE_OWNER AND o.OBJECT_NAME = s.TABLE_NAME
	WHERE s.OWNER IN ('PUBLIC', USER)
		AND o.OBJECT_TYPE IN ('TABLE', 'VIEW', 'PACKAGE', 'TYPE', 'PROCEDURE', 'FUNCTION', 'SEQUENCE')
	UNION ALL
	SELECT DB_LINK FROM ALL_DB_LINKS WHERE OWNER = USER OR OWNER = 'PUBLIC'`

var oracleDialect = &dialect{
	databases: func(ctx context.Context, c *Store, conn *Conn) ([]string, error) {
		return collectRows(ctx, conn, "SELECT name FROM v$database ORDER BY name", nil, scanString)
	},
	schemas: nil, // introspected through the connected user
	tables: func(ctx context.Context, c *Store, conn *Conn, database, schema string) ([]TableInfo, error) {
		return collectRows(ctx, conn, `SELECT OBJECT_NAME, OBJECT_TYPE, '' FROM ALL_OBJECTS
			WHERE OBJECT_TYPE IN ('TABLE','VIEW') AND OWNER = USER ORDER BY OBJECT_NAME`,
			nil, tableScanner(schema))
	},
	completionObjects: func(ctx context.Context, c *Store, conn *Conn, database string) ([]string, error) {
		return collectRows(ctx, conn, oracleCompletionObjectsSQL, nil, scanString)
	},
	columns: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ColumnInfo, error) {
		return collectRows(ctx, conn, `SELECT column_name, data_type, nullable, data_default, 0, comments
			FROM all_tab_columns c
			LEFT JOIN all_col_comments cc ON cc.owner=c.owner AND cc.table_name=c.table_name
				AND cc.column_name=c.column_name
			WHERE c.owner=USER AND c.table_name = :1 ORDER BY c.column_id`,
			[]interface{}{strings.ToUpper(table)}, scanStandardColumn)
	},
	search: func(ctx context.Context, c *Store, conn *Conn, database, fragment string, limit int) ([]ObjectMatch, error) {
		query := fmt.Sprintf(`SELECT * FROM (
			SELECT OWNER, OBJECT_NAME, OBJECT_TYPE FROM ALL_OBJECTS
			WHERE OBJECT_TYPE IN ('TABLE', 'VIEW') AND UPPER(OBJECT_NAME) LIKE '%s' ESCAPE '\'
			ORDER BY OWNER, OBJECT_NAME)
			WHERE ROWNUM <= %d`, strings.ReplaceAll(strings.ToUpper(likePattern(fragment)), "'", "''"), limit)
		return collectRows(ctx, conn, query, nil, matchScanner(conn, database))
	},
	foreignKeys: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]ForeignKeyInfo, error) {
		rows, err := collectRows(ctx, conn, fmt.Sprintf(`SELECT ac.constraint_name, cc.column_name, rac.owner, rac.table_name,
			rcc.column_name, ac.delete_rule, ''
			FROM ALL_CONSTRAINTS ac
			JOIN ALL_CONS_COLUMNS cc ON cc.owner = ac.owner AND cc.constraint_name = ac.constraint_name
			JOIN ALL_CONSTRAINTS rac ON rac.owner = ac.r_owner AND rac.constraint_name = ac.r_constraint_name
			JOIN ALL_CONS_COLUMNS rcc ON rcc.owner = rac.owner AND rcc.constraint_name = rac.constraint_name
				AND rcc.position = cc.position
			WHERE ac.constraint_type = 'R' AND ac.owner = USER AND ac.table_name = %s
			ORDER BY ac.constraint_name, cc.position`, oraLiteral(table)), nil, scanFkRow)
		if err != nil {
			return nil, err
		}
		return aggregateForeignKeys(rows), nil
	},
	fkValuesSQL: func(name, refColumn, labelColumn string, limit int) (string, error) {
		return fkValuesQueryLimit(DbTypeOracle, name, refColumn, labelColumn, limit,
			"OFFSET 0 ROWS FETCH NEXT %d ROWS ONLY")
	},
	qualifiedTable: func(database, schema, table string) (string, error) {
		return QualifiedName(DbTypeOracle, table)
	},
	indexes: func(ctx context.Context, c *Store, conn *Conn, database, schema, table string) ([]IndexInfo, error) {
		flat, err := collectRows(ctx, conn, fmt.Sprintf(`SELECT i.index_name, i.index_type,
			CASE WHEN i.uniqueness = 'UNIQUE' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS (SELECT 1 FROM ALL_CONSTRAINTS ac
				WHERE ac.constraint_type = 'P' AND ac.owner = i.owner AND ac.index_name = i.index_name)
			THEN 1 ELSE 0 END,
			c.column_name
			FROM ALL_INDEXES i
			JOIN ALL_IND_COLUMNS c ON c.index_owner = i.owner AND c.index_name = i.index_name
				AND c.table_owner = i.owner AND c.table_name = i.table_name
			WHERE i.table_name = %s AND i.owner = USER
			ORDER BY i.index_name, c.column_position`, oraLiteral(table)), nil, scanIndexRow)
		if err != nil {
			return nil, err
		}
		return aggregateIndexes(flat), nil
	},
	catalogSchema: func(database, schema string) string {
		return schema
	},
	tableSchema: func(schema string) string {
		return schema
	},
	// Oracle 12c+ takes the ANSI window clause and, unlike SQL Server, does not
	// require an ORDER BY in front of it.
	pageSQL:     offsetFetch,
	createTable: oracleCreateTable,
	// The INSERT ALL form repeats the whole column list per row, so the batch
	// is deliberately the smallest of the five: it is the one insert shape
	// here that no contract test of ours can run against a real server.
	insertRows:    insertAllInsert,
	maxInsertRows: 100,
}
