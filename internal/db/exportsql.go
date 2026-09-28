package db

import (
	"fmt"
	"strings"
)

// Ported from the parent TableProGo project's exportsql.go: render the grid
// back out as INSERT statements.

// insertExportMaxRows bounds what one export may render. The grid only ever
// holds a page of the table, so hitting this means the caller sent something
// it should not have - refusing is better than a script that quietly misses
// rows.
const insertExportMaxRows = 10000

// InsertScript renders one INSERT per row, with every literal written the way
// a save writes it: the column's declared type decides whether "7" is a number
// or text, and the dialect decides the quoting. Values are matched to columns
// by position, which is the only mapping a result that came out of this table
// has.
func InsertScript(
	dbType string,
	name string,
	table string,
	resultColumns []string,
	types map[string]string,
	rows [][]interface{},
) (string, error) {
	idents := make([]string, len(resultColumns))
	colTypes := make([]string, len(resultColumns))
	seen := make(map[string]bool, len(resultColumns))
	for i, col := range resultColumns {
		lower := strings.ToLower(col)
		if seen[lower] {
			return "", fmt.Errorf("结果里有同名列 %s，无法确定该写入哪个字段", col)
		}
		seen[lower] = true
		colType, ok := types[lower]
		if !ok {
			return "", fmt.Errorf(
				"结果列 %s 不是表 %s 的字段（别名或计算列还原不出 INSERT）", col, table)
		}
		ident, err := quoteIdent(dbType, col)
		if err != nil {
			return "", err
		}
		idents[i] = ident
		colTypes[i] = colType
	}

	statements := make([]string, 0, len(rows))
	for i, row := range rows {
		if len(row) != len(resultColumns) {
			return "", fmt.Errorf("第 %d 行有 %d 个值，和 %d 列对不上", i+1, len(row), len(resultColumns))
		}
		if len(statements) >= insertExportMaxRows {
			return "", fmt.Errorf("结果超过 %d 行，请先缩小查询范围再导出", insertExportMaxRows)
		}
		literals := make([]string, len(row))
		for j, value := range row {
			literal, err := valueLiteral(dbType, colTypes[j], value)
			if err != nil {
				return "", fmt.Errorf("第 %d 行 %s: %w", i+1, idents[j], err)
			}
			literals[j] = literal
		}
		statements = append(statements, fmt.Sprintf(
			"INSERT INTO %s (%s) VALUES (%s);",
			name, strings.Join(idents, ", "), strings.Join(literals, ", ")))
	}
	if len(statements) == 0 {
		return "", fmt.Errorf("没有可导出的行")
	}
	return strings.Join(statements, "\n") + "\n", nil
}
