package main

import (
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

type fkRow struct {
	name      string
	column    string
	refSchema string
	refTable  string
	refColumn string
	onDelete  string
	onUpdate  string
}

func aggregateForeignKeys(rows []fkRow) []ForeignKeyInfo {
	foreignKeys := []ForeignKeyInfo{}
	byName := map[string]int{}
	for _, row := range rows {
		i, ok := byName[row.name]
		if !ok {
			byName[row.name] = len(foreignKeys)
			foreignKeys = append(foreignKeys, ForeignKeyInfo{
				Name:      row.name,
				RefSchema: row.refSchema,
				RefTable:  row.refTable,
				OnDelete:  row.onDelete,
				OnUpdate:  row.onUpdate,
			})
			i = len(foreignKeys) - 1
		}
		foreignKeys[i].Columns = append(foreignKeys[i].Columns, row.column)
		foreignKeys[i].RefColumns = append(foreignKeys[i].RefColumns, row.refColumn)
	}
	for i := range foreignKeys {
		foreignKeys[i].OnDelete = referenceAction(foreignKeys[i].OnDelete)
		foreignKeys[i].OnUpdate = referenceAction(foreignKeys[i].OnUpdate)
	}
	return foreignKeys
}

// referenceAction renders a catalog action ("no_action", "CASCADE", "RESTRICT")
// the way it is written in DDL.
func referenceAction(raw string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(raw)), "_", " ")
}

func oraLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// tableForeignKeys reads the foreign-key constraints of one table; the
// per-engine catalog queries live on the dialect (dialect.go).
func (c *Client) tableForeignKeys(conn *Conn, database, schema, table string) ([]ForeignKeyInfo, error) {
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		return nil, err
	}
	return d.foreignKeys(c, conn, database, schema, table)
}

// foreignKeys lists the constraints a table has, memoized per table for the
// session: the cell dialog opens on every edit and the catalog rarely changes.
func (c *Client) foreignKeys(connID, database, schema, table string) ([]ForeignKeyInfo, error) {
	conn, err := c.openConn(connID)
	if err != nil {
		return nil, err
	}
	if schema == "" {
		schema = resolveSchema(conn.Config.Type, database, schema)
	}
	key := connID + "\x00" + database + "\x00" + schema + "\x00" + table
	c.mu.Lock()
	cached, ok := c.fkCache[key]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}
	fks, err := c.tableForeignKeys(conn, database, schema, table)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.fkCache[key] = fks
	c.mu.Unlock()
	return fks, nil
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
	if dbType == dbTypeMySQL {
		// In MySQL's catalog the "schema" of a reference is a database name.
		if refSchema == "" {
			refSchema = database
		}
		return tableName(dbType, refSchema, "", refTable)
	}
	return tableName(dbType, database, defaultSchema(dbType, refSchema), refTable)
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

// fkValuesQueryLimit splices the engine's row limiter into the shared shape.
// Ordering on the key is what makes consecutive reads agree, and DISTINCT only
// allows it because the key is in the select list.
func fkValuesQueryLimit(dbType, name, refColumn, labelColumn string, limit int, rowLimit string) (string, error) {
	if limit <= 0 {
		return "", fmt.Errorf("invalid limit %d", limit)
	}
	key, err := quoteIdent(dbType, refColumn)
	if err != nil {
		return "", err
	}
	selected := key
	if labelColumn != "" {
		label, err := quoteIdent(dbType, labelColumn)
		if err != nil {
			return "", err
		}
		selected += ", " + label
	}
	return fmt.Sprintf("SELECT DISTINCT %s FROM %s ORDER BY %s "+rowLimit, selected, name, key, limit), nil
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
func (c *Client) foreignKeyValues(connID, database, schema, table, column string, limit int) (ForeignKeyCandidates, error) {
	conn, err := c.openConn(connID)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}
	if limit <= 0 || limit > fkValuesMaxLimit {
		limit = fkValuesDefaultLimit
	}
	if schema == "" {
		schema = resolveSchema(conn.Config.Type, database, schema)
	}

	fks, err := c.foreignKeys(connID, database, schema, table)
	if err != nil {
		return ForeignKeyCandidates{}, err
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
	if conn.Config.Type == dbTypeMySQL && refSchema == "" {
		refSchema = database
	}
	// The description is a convenience: a catalog the database filter hides must
	// not cost the user their list of keys.
	var labelColumn string
	if columns, err := c.columns(connID, database, refSchema, fk.RefTable); err == nil {
		labelColumn = pickLabelColumn(columns, refColumn)
	}

	query, err := fkValuesQuery(conn.Config.Type, name, refColumn, labelColumn, limit)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}

	db := conn.DB
	if conn.Config.Type == dbTypePostgres {
		if db, err = c.poolFor(conn, database); err != nil {
			return ForeignKeyCandidates{}, err
		}
	}
	result := runSelect(context.Background(), db, query, time.Now())
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
