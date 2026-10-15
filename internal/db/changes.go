package db

import (
	"github.com/Ben-guanzhi/TableProGo/tableprosql"

	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Ported from the parent TableProGo project's changes.go: the grid's edit
// engine. Nothing here trusts the client to send SQL — statements are rebuilt
// from the grid's values on every preview and apply.

// TableRef names one object the way the client addresses it.
type TableRef struct {
	ConnID   string
	Database string
	Schema   string
	Table    string
}

// starSelectRe matches the only shape whose rows can be written back: every
// column of one table. A projection picks fields, an aggregate picks groups,
// and neither has a row to update.
var starSelectRe = regexp.MustCompile("(?is)^\\s*select\\s+(?:distinct\\s+)?(?:[\\w.$@#\\[\\]\"`]+\\.)?\\*\\s+from\\s+([\\w.$@#\\[\\]\"`]+)")

var multiTableRe = regexp.MustCompile(`(?is)\b(join|group\s+by|having|union)\b`)

// querySource reports the table a statement reads rows from, which is how the
// grid knows it may edit them. The zero ref - no table - means read-only.
func (c *Store) QuerySource(connID, query, database string) TableRef {
	conn, err := c.OpenConn(connID)
	if err != nil {
		return TableRef{}
	}
	if database == "" {
		database = conn.Config.Database
	}
	return parseQuerySource(connID, query, database)
}

// parseQuerySource is the text half of that, split out so the shapes can be
// read without a live connection. database is already the fallback to use.
func parseQuerySource(connID, query, database string) TableRef {
	text := sqlCommentRe.ReplaceAllString(query, " ")
	if !isSelectQuery(text) || multiTableRe.MatchString(text) {
		return TableRef{}
	}
	match := starSelectRe.FindStringSubmatch(text)
	if match == nil {
		return TableRef{}
	}
	ref := parseTableRef(match[1])
	if ref.table == "" || !isIdentifierLike(ref.table) {
		return TableRef{}
	}
	if ref.database != "" {
		database = ref.database
	}
	return TableRef{ConnID: connID, Database: database, Schema: ref.schema, Table: ref.table}
}

// tableRef is the parsed database/schema/table triple of a SQL text reference.
type tableRef struct {
	database, schema, table string
}

// unquoteIdentPart strips one bracketed/quoted identifier part.
func unquoteIdentPart(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}
	if len(part) >= 2 && part[0] == '[' && part[len(part)-1] == ']' {
		return strings.ReplaceAll(part[1:len(part)-1], "]]", "]")
	}
	quote := part[0]
	if len(part) >= 2 && (quote == '`' || quote == '"' || quote == '\'') && part[len(part)-1] == quote {
		return strings.ReplaceAll(part[1:len(part)-1], string([]byte{quote, quote}), string(quote))
	}
	return part
}

func parseTableRef(raw string) tableRef {
	parts := strings.Split(strings.Trim(raw, " \t\r\n"), ".")
	for i, part := range parts {
		parts[i] = unquoteIdentPart(part)
	}

	var ref tableRef
	switch len(parts) {
	case 1:
		ref.table = parts[0]
	case 2:
		ref.schema, ref.table = parts[0], parts[1]
	default:
		ref.database, ref.schema, ref.table = parts[len(parts)-3], parts[len(parts)-2], parts[len(parts)-1]
	}
	return ref
}

// CellEdit is one value typed into the result grid. The column is named as the
// result named it, so the builder can tell an alias from a real field.
type CellEdit struct {
	Column string
	Value  interface{}
}

// How a queued row touches the table. An edited cell is the default, so a
// caller that says nothing means an UPDATE.
const (
	RowUpdate = ""
	RowInsert = "insert"
	RowDelete = "delete"
)

// RowChange carries the row exactly as the grid read it, which is how the key
// columns are picked out without the caller having to know which they are. A
// new row has no original, and only the values the user actually typed count
// as its edits.
type RowChange struct {
	Kind     string
	Original []interface{}
	Edits    []CellEdit
}

// ChangeSet describes the edits made to one table. Nothing in it is SQL: both
// the preview and the apply rebuild the statements here.
type ChangeSet struct {
	ConnID   string
	Database string
	Schema   string
	Table    string
	Columns  []string
	Changes  []RowChange
}

// TableName renders the object the way the grid addressed it, so an update
// hits exactly what is on screen; the per-engine addressing lives on the
// dialect (dialect.go).
func TableName(dbType, database, schema, table string) (string, error) {
	d, err := dialectFor(dbType)
	if err != nil {
		return "", err
	}
	return d.qualifiedTable(database, schema, table)
}

// DefaultSchema fills the engine's default table namespace for qualified
// names; the per-engine answers live in the shared SQL text package.
func DefaultSchema(dbType, schema string) string {
	return tableprosql.DefaultTableSchema(dbType, schema)
}

// isNumericColumn keeps the grid's historical name; the type knowledge lives
// in the shared SQL text package, so the Fyne client, the root client and the
// web filter bar all answer "does this column hold numbers" the same way.
func isNumericColumn(colType string) bool {
	return tableprosql.NewColumnType(colType).IsNumeric()
}

func numberLiteral(value float64) string {
	if math.Trunc(value) == value && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	// Never scientific notation: "1e+18" is not what every engine accepts.
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// wholeNumberLiteral renders text that denotes a plain whole number exactly and
// reports whether it recognised that shape. The float path can't: a double keeps
// 53 bits of mantissa, so "9007199254740993" comes back one lower and a key
// predicate built on it names a neighbouring row or none.
//
// Digits past what 64 bits hold go out as the digits themselves rather than as a
// rounded float or a refusal - a server reads a numeric literal at its own
// precision, so a DECIMAL(30,0) column still gets the value the user named, while
// a bigint column answers an impossible one with its own out-of-range error. That
// error is the outcome the client must not pre-empt by quietly rounding: it is
// loud, and it names the column.
//
// Only the shape is recognised, never the size: an optional sign and nothing but
// digits, so "1e3" and "1.50" and "0x10" stay on the path that has always
// rendered them (or refused them). That scan is also what keeps the verbatim
// branch above from doing damage: without it, "twelve" in a numeric cell would go
// out as the literal twelve rather than being refused, and any text a cell holds
// would reach the server unquoted.
func wholeNumberLiteral(text string) (string, bool) {
	body := strings.TrimPrefix(text, "+")
	digits := strings.TrimPrefix(body, "-")
	if digits == "" {
		return "", false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return "", false
		}
	}
	if number, err := strconv.ParseInt(text, 10, 64); err == nil {
		return strconv.FormatInt(number, 10), true
	}
	if !strings.HasPrefix(body, "-") {
		if number, err := strconv.ParseUint(digits, 10, 64); err == nil {
			return strconv.FormatUint(number, 10), true
		}
	}
	return body, true
}

func quotedLiteral(dbType, value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	// Only SQL Server needs the national prefix to keep wide characters, and
	// PostgreSQL rejects it, so the rest get a plain single-quoted literal.
	if dbType == DbTypeSQLServer {
		return "N'" + escaped + "'"
	}
	return "'" + escaped + "'"
}

// valueLiteral turns what the grid holds into a literal for the column it
// belongs to. The declared type decides whether text is a number, which is what
// lets a cleared numeric cell mean NULL instead of zero.
//
// The switch below answers the shapes a read can actually leave in a cell:
// runSelect is the only reader the plain and the paged grid go through, and it
// folds the driver's bytes and times into text and passes everything else
// through. What the drivers hand back is therefore what has to be answered
// here - go-sqlite3 measured in TestApplyChangesWritesBackAReadRow, lib/pq
// decoding every integer width to int64 (encode.go's textDecode/binaryDecode)
// and go-sql-driver's text rows answering with int64, uint64 for an unsigned
// BIGINT and float32 for FLOAT (packets.go's readRow). A type outside this set
// is refused rather than guessed at, so a driver that starts answering a new
// shape fails loudly instead of writing a value it never read.
//
// Text is the other half, and it is not a read at all: it is what someone typed
// into a cell or the filter bar, so the column's declared type decides whether it
// is a number. A whole number keeps its digits from there - see
// wholeNumberLiteral - because a person who typed a 17-digit key means that key,
// and the float path would hand the server a different one.
func valueLiteral(dbType, colType string, value interface{}) (string, error) {
	switch v := value.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case int64:
		// The grid hands the row it read straight to the key predicate, so an
		// integer primary key reaches here as an int64 - see
		// TestApplyChangesWritesBackAReadRow. It must not be reformatted as a
		// float64: past 2^53 the float cannot hold the key, so the WHERE would
		// locate a different row or none.
		return strconv.FormatInt(v, 10), nil
	case uint64:
		// Same reason, and the same 2^53 argument: an unsigned BIGINT key is
		// past what a float64 can name exactly, so it is written from the integer.
		return strconv.FormatUint(v, 10), nil
	case float64:
		return numberLiteral(v), nil
	case float32:
		// Widening is exact, so the shortest decimal for the double names the
		// same float the server stores back from that literal.
		return numberLiteral(float64(v)), nil
	case string:
		if isNumericColumn(colType) {
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				return "NULL", nil
			}
			if literal, whole := wholeNumberLiteral(trimmed); whole {
				return literal, nil
			}
			number, err := strconv.ParseFloat(trimmed, 64)
			if err != nil {
				return "", fmt.Errorf("%q is not a valid %s", v, colType)
			}
			return numberLiteral(number), nil
		}
		if strings.ContainsRune(v, 0) {
			return "", fmt.Errorf("a value cannot contain NUL")
		}
		return quotedLiteral(dbType, v), nil
	}
	return "", fmt.Errorf("unsupported value of type %T", value)
}

// predicate renders one "column = value" test, with NULL compared the only way
// SQL allows.
func predicate(dbType, colType, quotedColumn string, value interface{}) (string, error) {
	literal, err := valueLiteral(dbType, colType, value)
	if err != nil {
		return "", err
	}
	if literal == "NULL" {
		return quotedColumn + " IS NULL", nil
	}
	return quotedColumn + " = " + literal, nil
}

// ChangeContext holds what every statement in a batch needs: where each result
// column came from and how a row is located. Building it once means a
// projection that left the key out is caught before any statement is rendered.
type ChangeContext struct {
	dbType       string
	name         string
	columns      []string
	positions    map[string]int
	keyPositions []int
	types        map[string]string
}

func NewChangeContext(dbType, name string, resultColumns, keyColumns []string, types map[string]string) (*ChangeContext, error) {
	c := &ChangeContext{
		dbType:    dbType,
		name:      name,
		columns:   resultColumns,
		types:     types,
		positions: make(map[string]int, len(resultColumns)),
	}
	for i, col := range resultColumns {
		c.positions[strings.ToLower(col)] = i
	}
	// A table with no key is still usable here: only the statements that locate
	// an existing row by key need one, and an insert does not.
	for _, key := range keyColumns {
		i, ok := c.positions[strings.ToLower(key)]
		if !ok {
			return nil, fmt.Errorf("the result does not include key column %s; select it to edit rows", key)
		}
		c.keyPositions = append(c.keyPositions, i)
	}
	return c, nil
}

// resolve matches a result column against the table, which is how an alias or
// a computed field is caught before it reaches the server.
func (c *ChangeContext) resolve(resultName string) (string, string, error) {
	i, ok := c.positions[strings.ToLower(resultName)]
	if !ok {
		return "", "", fmt.Errorf("column %s is not in the result", resultName)
	}
	colType, ok := c.types[strings.ToLower(c.columns[i])]
	if !ok {
		return "", "", fmt.Errorf("%s is not a plain column of the table, so it cannot be edited", c.columns[i])
	}
	ident, err := quoteIdent(c.dbType, c.columns[i])
	if err != nil {
		return "", "", err
	}
	return ident, colType, nil
}

func (c *ChangeContext) literals(edits []CellEdit) ([]string, []string, []string, error) {
	columns := make([]string, 0, len(edits))
	values := make([]string, 0, len(edits))
	assignments := make([]string, 0, len(edits))
	for _, edit := range edits {
		ident, colType, err := c.resolve(edit.Column)
		if err != nil {
			return nil, nil, nil, err
		}
		literal, err := valueLiteral(c.dbType, colType, edit.Value)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", edit.Column, err)
		}
		columns = append(columns, ident)
		values = append(values, literal)
		assignments = append(assignments, ident+" = "+literal)
	}
	return columns, values, assignments, nil
}

// keyWhere is the "WHERE" every write that finds an existing row needs, with
// NULL compared the only way SQL allows.
func (c *ChangeContext) keyWhere(original []interface{}) (string, error) {
	if len(c.keyPositions) == 0 {
		return "", fmt.Errorf("%s has no primary key, so rows cannot be located", c.name)
	}
	conditions := make([]string, 0, len(c.keyPositions))
	for _, i := range c.keyPositions {
		ident, colType, err := c.resolve(c.columns[i])
		if err != nil {
			return "", err
		}
		var value interface{}
		if i < len(original) {
			value = original[i]
		}
		test, err := predicate(c.dbType, colType, ident, value)
		if err != nil {
			return "", err
		}
		conditions = append(conditions, test)
	}
	return strings.Join(conditions, " AND "), nil
}

// update returns an empty statement for a row whose edits all cancelled out.
func (c *ChangeContext) update(change RowChange) (string, error) {
	_, _, assignments, err := c.literals(change.Edits)
	if err != nil {
		return "", err
	}
	if len(assignments) == 0 {
		return "", nil
	}
	where, err := c.keyWhere(change.Original)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("UPDATE %s SET %s WHERE %s", c.name, strings.Join(assignments, ", "), where), nil
}

// insert writes only the columns the grid filled in, so an auto-increment key
// and every column default keep working.
func (c *ChangeContext) insert(change RowChange) (string, error) {
	if len(change.Edits) == 0 {
		return "", fmt.Errorf("a new row needs at least one value")
	}
	columns, values, _, err := c.literals(change.Edits)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		c.name, strings.Join(columns, ", "), strings.Join(values, ", ")), nil
}

func (c *ChangeContext) remove(change RowChange) (string, error) {
	where, err := c.keyWhere(change.Original)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("DELETE FROM %s WHERE %s", c.name, where), nil
}

// BuildStatements is the pure half of the work: given how the result is shaped
// and what the table's columns are, render the whole queue - UPDATE, INSERT and
// DELETE rows in the order the client queued them.
func BuildStatements(
	dbType string,
	name string,
	resultColumns []string,
	keyColumns []string,
	types map[string]string,
	changes []RowChange,
) ([]string, error) {
	if len(changes) == 0 {
		return nil, fmt.Errorf("nothing to save")
	}
	c, err := NewChangeContext(dbType, name, resultColumns, keyColumns, types)
	if err != nil {
		return nil, err
	}

	statements := make([]string, 0, len(changes))
	for _, change := range changes {
		var statement string
		switch change.Kind {
		case RowInsert:
			statement, err = c.insert(change)
		case RowDelete:
			statement, err = c.remove(change)
		case RowUpdate:
			statement, err = c.update(change)
		default:
			// A kind nobody recognises must not quietly become an UPDATE: the
			// row would be written differently from what the grid promised.
			return nil, fmt.Errorf("unknown row change kind %q", change.Kind)
		}
		if err != nil {
			return nil, err
		}
		if statement != "" {
			statements = append(statements, statement)
		}
	}
	if len(statements) == 0 {
		return nil, fmt.Errorf("nothing to save")
	}
	return statements, nil
}

// buildChanges resolves what the grid cannot know - the object's name and the
// table's keys and types - and leaves the rest to the pure builder.
func (c *Store) BuildChanges(set ChangeSet) ([]string, error) {
	conn, err := c.OpenConn(set.ConnID)
	if err != nil {
		return nil, err
	}
	if set.Table == "" {
		return nil, fmt.Errorf("no table to write to")
	}
	schema := DefaultSchema(conn.Config.Type, set.Schema)

	name, err := TableName(conn.Config.Type, set.Database, schema, set.Table)
	if err != nil {
		return nil, err
	}
	columns, err := c.Columns(set.ConnID, set.Database, schema, set.Table)
	if err != nil {
		return nil, err
	}

	types := make(map[string]string, len(columns))
	var keys []string
	for _, col := range columns {
		types[strings.ToLower(col.Name)] = col.Type
		if col.IsPrimaryKey {
			keys = append(keys, col.Name)
		}
	}
	return BuildStatements(conn.Config.Type, name, set.Columns, keys, types, set.Changes)
}

// ApplyChanges runs the rebuilt statements as one transaction: either every
// edit lands or none does, which is the only way a mistake stays fixable. A
// statement that matches no row aborts the batch instead of quietly skipping.
// The count is the rows the batch committed, so it is 0 for any failed run -
// including the one that aborted on a statement matching no row, whose earlier
// statements had already been rolled back by the time this returns.
func (c *Store) ApplyChanges(set ChangeSet) (int64, error) {
	statements, err := c.BuildChanges(set)
	if err != nil {
		return 0, err
	}
	conn, err := c.OpenConn(set.ConnID)
	if err != nil {
		return 0, err
	}
	db, err := c.queryPool(conn, set.Database)
	if err != nil {
		return 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var applied int64
	for i, statement := range statements {
		affected, err := execInTx(tx, statement, i)
		if err != nil {
			return 0, err
		}
		applied += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return applied, nil
}

func execInTx(tx *sql.Tx, statement string, index int) (int64, error) {
	res, err := tx.Exec(statement)
	if err != nil {
		return 0, fmt.Errorf("statement %d: %w", index+1, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("statement %d: %w", index+1, err)
	}
	if affected == 0 {
		return 0, fmt.Errorf("statement %d matched no row; the data changed since it was read, nothing was committed", index+1)
	}
	return affected, nil
}
