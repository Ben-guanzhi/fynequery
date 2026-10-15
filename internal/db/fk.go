package db

import (
	"github.com/Ben-guanzhi/TableProGo/tableprosql"

	"context"
	"fmt"
	"strings"
	"time"
)

// Ported from the parent TableProGo project (fkvalues.go and the catalog half
// of main.go): foreign-key metadata and the candidate values a foreign-key
// cell may take.

// ForeignKeyInfo describes one constraint pointing at another table; composite
// keys keep their column pairs in catalog order.
type ForeignKeyInfo struct {
	Name       string
	Columns    []string
	RefSchema  string
	RefTable   string
	RefColumns []string
	OnDelete   string
	OnUpdate   string
}

// The flat FK row is shared as tableprosql.FKRow; the catalog readers in
// dialect.go scan into it and aggregateForeignKeys folds it into ForeignKeyInfo.

func aggregateForeignKeys(rows []tableprosql.FKRow) []ForeignKeyInfo {
	foreignKeys := []ForeignKeyInfo{}
	byName := map[string]int{}
	for _, row := range rows {
		i, ok := byName[row.Name]
		if !ok {
			byName[row.Name] = len(foreignKeys)
			foreignKeys = append(foreignKeys, ForeignKeyInfo{
				Name:      row.Name,
				RefSchema: row.RefSchema,
				RefTable:  row.RefTable,
				OnDelete:  row.OnDelete,
				OnUpdate:  row.OnUpdate,
			})
			i = len(foreignKeys) - 1
		}
		foreignKeys[i].Columns = append(foreignKeys[i].Columns, row.Column)
		foreignKeys[i].RefColumns = append(foreignKeys[i].RefColumns, row.RefColumn)
	}
	for i := range foreignKeys {
		foreignKeys[i].OnDelete = tableprosql.ReferenceAction(foreignKeys[i].OnDelete)
		foreignKeys[i].OnUpdate = tableprosql.ReferenceAction(foreignKeys[i].OnUpdate)
	}
	return foreignKeys
}

func oraLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// tableForeignKeys reads the foreign-key constraints of one table; the
// per-engine catalog queries live on the dialect (dialect.go).
func (c *Store) TableForeignKeys(ctx context.Context, conn *Conn, database, schema, table string) ([]ForeignKeyInfo, error) {
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		return nil, err
	}
	return d.foreignKeys(ctx, c, conn, database, schema, table)
}

// ForeignKeys lists the constraints a table has. Memoization is the UI's
// job (fkCache in main): the catalog readers stay pure here.
func (c *Store) ForeignKeys(ctx context.Context, connID, database, schema, table string) ([]ForeignKeyInfo, error) {
	conn, err := c.OpenConn(connID)
	if err != nil {
		return nil, err
	}
	if schema == "" {
		schema = ResolveSchema(conn.Config.Type, database, schema)
	}
	return c.TableForeignKeys(ctx, conn, database, schema, table)
}

// ForeignKeyValue is one choice for a foreign-key cell: the key the cell would
// store, plus a short description of the row behind it, so "7" and "12" can be
// told apart without opening the other table.
type ForeignKeyValue struct {
	Value string
	Label string
}

// ForeignKeyCandidates answers "what could this cell hold".
type ForeignKeyCandidates struct {
	RefTable    string
	RefColumn   string
	LabelColumn string
	Values      []ForeignKeyValue
	Truncated   bool
}

const (
	fkValuesDefaultLimit = 200
	fkValuesMaxLimit     = 500
)

// singleColumnForeignKey finds the constraint that makes column a reference to
// exactly one column of one other table. A composite key is refused on purpose:
// one cell cannot hold half a key, so the grid keeps plain text entry there.
func singleColumnForeignKey(fks []ForeignKeyInfo, column string) (ForeignKeyInfo, error) {
	for _, fk := range fks {
		matched := false
		for _, name := range fk.Columns {
			if strings.EqualFold(name, column) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 {
			return fk, nil
		}
		return ForeignKeyInfo{}, fmt.Errorf(
			"%s 是复合外键 %s 的一部分，单元格只能自己填值", column, strings.Join(fk.Columns, "+"))
	}
	return ForeignKeyInfo{}, fmt.Errorf("%s 没有外键", column)
}

// fkTargetName renders the referenced object the way this dialect addresses
// tables, reusing the name builder the update statements use so the list is read
// from the very object an edit would write.
func fkTargetName(dbType, database, refSchema, refTable string) (string, error) {
	if dbType == DbTypeMySQL {
		// In MySQL's catalog the "schema" of a reference is a database name.
		if refSchema == "" {
			refSchema = database
		}
		return TableName(dbType, refSchema, "", refTable)
	}
	return TableName(dbType, database, DefaultSchema(dbType, refSchema), refTable)
}

// RefName renders a constraint's target the way this dialect addresses tables,
// for listings that have to answer "which table does this point at". A name the
// dialect cannot build (unknown engine) falls back to the bare table rather than
// costing the caller the whole row.
func RefName(dbType, database string, fk ForeignKeyInfo) string {
	name, err := fkTargetName(dbType, database, fk.RefSchema, fk.RefTable)
	if err != nil {
		return fk.RefTable
	}
	return name
}

// isDescriptiveType reports whether a column holds something a person reads.
// Numbers, dates and blobs describe a row only to whoever already knows it.
func isDescriptiveType(colType string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.Split(colType, "(")[0]))
	if strings.HasSuffix(base, "text") || strings.HasSuffix(base, "char") {
		return true
	}
	switch base {
	case "varchar", "nvarchar", "character", "character varying", "citext", "string",
		"uuid", "uniqueidentifier", "name", "tinytext", "json", "jsonb":
		return true
	}
	return false
}

// pickLabelColumn chooses the description column: the first readable text column
// that is neither the key being stored nor part of any key. A table with nothing
// like that gets no description at all rather than an id echoing itself.
func pickLabelColumn(columns []ColumnInfo, refColumn string) string {
	for _, col := range columns {
		if col.IsPrimaryKey || strings.EqualFold(col.Name, refColumn) {
			continue
		}
		if isDescriptiveType(col.Type) {
			return col.Name
		}
	}
	return ""
}

// fkValuesQuery renders the candidate-keys query for an engine; the shape is
// shared and only the row limiter differs per dialect.
func fkValuesQuery(dbType, name, refColumn, labelColumn string, limit int) (string, error) {
	d, err := dialectFor(dbType)
	if err != nil {
		return "", err
	}
	return d.fkValuesSQL(name, refColumn, labelColumn, limit)
}

// fkValuesQueryLimit renders the candidate-keys query; the shapes live in the
// shared SQL text package. Ordering on the key is what makes consecutive reads
// agree, and DISTINCT only allows it because the key is in the select list.
func fkValuesQueryLimit(dbType, name, refColumn, labelColumn string, limit int, _ string) (string, error) {
	return tableprosql.FKValuesQuery(dbType, name, refColumn, labelColumn, limit)
}

// candidateText renders a key the way the grid shows it, so the text picked from
// the list is the text the cell then holds.
func candidateText(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return numberLiteral(v)
	case bool:
		if v {
			return "1"
		}
		return "0"
	}
	return fmt.Sprintf("%v", value)
}

// foreignKeyValues lists the values a foreign-key cell may take. It reads the
// catalog first, so a column that is not a single-column reference is refused
// before anything is queried.
func (c *Store) ForeignKeyValues(ctx context.Context, connID, database, schema, table, column string, fks []ForeignKeyInfo, limit int) (ForeignKeyCandidates, error) {
	conn, err := c.OpenConn(connID)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}
	if limit <= 0 || limit > fkValuesMaxLimit {
		limit = fkValuesDefaultLimit
	}
	if schema == "" {
		schema = ResolveSchema(conn.Config.Type, database, schema)
	}

	fk, err := singleColumnForeignKey(fks, column)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}
	refColumn := fk.RefColumns[0]

	name, err := fkTargetName(conn.Config.Type, database, fk.RefSchema, fk.RefTable)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}

	refSchema := fk.RefSchema
	if conn.Config.Type == DbTypeMySQL && refSchema == "" {
		refSchema = database
	}
	// The description is a convenience: a catalog the database filter hides must
	// not cost the user their list of keys.
	var labelColumn string
	if columns, err := c.Columns(ctx, connID, database, refSchema, fk.RefTable); err == nil {
		labelColumn = pickLabelColumn(columns, refColumn)
	}

	query, err := fkValuesQuery(conn.Config.Type, name, refColumn, labelColumn, limit)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}

	// The same pool the catalog readers use: PostgreSQL needs a session inside
	// the referenced database, the others answer over the main handle - and both
	// now go through the lock, so a disconnect during this call is an error
	// rather than a handle that vanished underneath it.
	db, err := c.poolFor(ctx, conn, database)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}
	// The caller's context reaches this query: the candidate list is fetched
	// while the user is waiting on a dialog, and a slow server must be
	// interruptible rather than fatal.
	result := runSelect(ctx, db, query, time.Now())
	if result.Error != "" {
		return ForeignKeyCandidates{}, fmt.Errorf("%s", result.Error)
	}

	values := make([]ForeignKeyValue, 0, result.RowCount)
	seen := map[string]bool{}
	for _, row := range result.Rows {
		value := candidateText(row[0])
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		label := ""
		if len(row) > 1 {
			label = candidateText(row[1])
		}
		values = append(values, ForeignKeyValue{Value: value, Label: label})
	}

	return ForeignKeyCandidates{
		RefTable:    fk.RefTable,
		RefColumn:   refColumn,
		LabelColumn: labelColumn,
		Values:      values,
		Truncated:   result.RowCount >= limit,
	}, nil
}
