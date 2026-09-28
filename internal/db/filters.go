package db

import (
	"fmt"
	"strings"
)

// Ported from the parent TableProGo project's filters.go: the data page's
// filter bar. The bar can name a column but never write SQL, so the WHERE is
// rebuilt here the same way an update is - which is what lets a filtered page
// still be edited and written back to the rows it shows.

// TableFilter is one condition: a column of the table, how it is compared, and
// what it is compared with.
type TableFilter struct {
	Column string
	Op     string
	Value  string
}

// The comparisons the bar offers. Each is spelled out below; anything else is
// refused rather than guessed at.
const (
	FilterEq       = "eq"
	FilterNe       = "ne"
	FilterGT       = "gt"
	FilterGTE      = "gte"
	FilterLT       = "lt"
	FilterLTE      = "lte"
	FilterContains = "contains"
	FilterIsNull   = "null"
	FilterNotNull  = "notNull"
)

// likeEscape is the ESCAPE character of a contains test. Backslash would give
// the pattern a second meaning inside a MySQL string literal, so the wildcards
// are escaped with a character no engine treats specially.
const likeEscape = "/"

// FilterValueNeeded lists the comparisons that compare against nothing; every
// other one needs something on its right.
func FilterValueNeeded(op string) bool {
	return op != FilterIsNull && op != FilterNotNull
}

// ContainsPattern turns what the user typed into a LIKE pattern: the characters
// that would otherwise be wildcards are escaped first, so "100%" searches for
// the text "100%" rather than everything.
func ContainsPattern(value string) string {
	escaped := strings.NewReplacer(likeEscape, likeEscape+likeEscape, "%", likeEscape+"%", "_", likeEscape+"_").Replace(value)
	return "%" + escaped + "%"
}

// filterCondition renders one condition. colType is the column's declared type,
// which decides whether a typed value is a number or a string.
func filterCondition(dbType, colType, quoted string, f TableFilter) (string, error) {
	switch f.Op {
	case FilterIsNull:
		return quoted + " IS NULL", nil
	case FilterNotNull:
		return quoted + " IS NOT NULL", nil
	case FilterContains:
		if isNumericColumn(colType) {
			return "", fmt.Errorf("“包含”不适用于 %s 列，改用等于或大小比较", colType)
		}
		return quoted + " LIKE " + quotedLiteral(dbType, ContainsPattern(f.Value)) +
			" ESCAPE " + quotedLiteral(dbType, likeEscape), nil
	case FilterEq, FilterNe, FilterGT, FilterGTE, FilterLT, FilterLTE:
	default:
		return "", fmt.Errorf("unknown filter operator %q", f.Op)
	}

	literal, err := valueLiteral(dbType, colType, f.Value)
	if err != nil {
		return "", err
	}
	// An empty value in a numeric column is NULL, and "col > NULL" is neither
	// true nor false - it silently matches nothing. Only equality can mean an
	// absent value, so the rest are refused while empty.
	if literal == "NULL" {
		switch f.Op {
		case FilterEq:
			return quoted + " IS NULL", nil
		case FilterNe:
			return quoted + " IS NOT NULL", nil
		}
		return "", fmt.Errorf("这个比较需要一个值，否则不会匹配任何行")
	}
	return quoted + " " + SqlComparison(f.Op) + " " + literal, nil
}

func SqlComparison(op string) string {
	switch op {
	case FilterNe:
		return "<>"
	case FilterGT:
		return ">"
	case FilterGTE:
		return ">="
	case FilterLT:
		return "<"
	case FilterLTE:
		return "<="
	}
	return "="
}

// tableWhere renders the bar's conditions into the WHERE of the data page's
// query. Columns are matched against the catalog, ignoring case, so the only
// names that reach a statement are ones the table actually has. Conditions are
// ANDed: the bar narrows the page, and a row has to satisfy every condition on
// it.
func (c *Store) TableWhere(connID, database, schema, table string, filters []TableFilter) (string, error) {
	if len(filters) == 0 {
		return "", nil
	}
	conn, err := c.OpenConn(connID)
	if err != nil {
		return "", err
	}
	schema = DefaultSchema(conn.Config.Type, schema)
	columns, err := c.Columns(connID, database, schema, table)
	if err != nil {
		return "", err
	}
	byName := make(map[string]ColumnInfo, len(columns))
	for _, col := range columns {
		byName[strings.ToLower(col.Name)] = col
	}

	conditions := make([]string, 0, len(filters))
	for _, filter := range filters {
		col, ok := byName[strings.ToLower(strings.TrimSpace(filter.Column))]
		if !ok {
			return "", fmt.Errorf("%s is not a column of %s", filter.Column, table)
		}
		quoted, err := quoteIdent(conn.Config.Type, col.Name)
		if err != nil {
			return "", err
		}
		condition, err := filterCondition(conn.Config.Type, col.Type, quoted, filter)
		if err != nil {
			return "", fmt.Errorf("%s: %w", col.Name, err)
		}
		conditions = append(conditions, condition)
	}
	return " WHERE " + strings.Join(conditions, " AND "), nil
}
