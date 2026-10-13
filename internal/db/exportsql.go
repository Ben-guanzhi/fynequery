package db

import (
	"fmt"
	"strings"
)

// Ported from the parent TableProGo project's exportsql.go: render the grid
// back out as INSERT statements.

// insertExportMaxRows bounds what one INSERT-script export may render. The grid
// holds at most a page - paging tops out at 5000 rows - so this is two pages of
// headroom. The xlsx export allows ten times as many because a workbook is a
// compressed archive and a script is plain text that grows with the row count.
// The caps differ on purpose; each refusal names its own format.
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
	// Checked up front, not inside the render loop: the root app checks the same
	// bound on the same rows slice before rendering, and a refusal that costs the
	// work of rendering nine thousand nine hundred and ninety-nine rows first is
	// not a refusal, it is a slow answer. len(rows) > max keeps exactly max rows
	// allowed, same as the root's comparison.
	if len(rows) > insertExportMaxRows {
		return "", fmt.Errorf("一次最多导出 %d 行（INSERT 脚本），请翻页或缩小范围", insertExportMaxRows)
	}
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

	// The head is the same text in every row: joining the column list per row
	// is one throwaway string per row for a constant, and rendering into one
	// grown builder instead of a slice of statements plus a final Join drops
	// the second full copy of the script. Mirrored from the root client's
	// exportsql.go so both exports render byte-identical text.
	head := "INSERT INTO " + name + " (" + strings.Join(idents, ", ") + ") VALUES ("
	var script strings.Builder
	script.Grow(len(rows) * (len(head) + 32))
	for i, row := range rows {
		if len(row) != len(resultColumns) {
			return "", fmt.Errorf("第 %d 行有 %d 个值，和 %d 列对不上", i+1, len(row), len(resultColumns))
		}
		script.WriteString(head)
		for j, value := range row {
			literal, err := valueLiteral(dbType, colTypes[j], value)
			if err != nil {
				return "", fmt.Errorf("第 %d 行 %s: %w", i+1, idents[j], err)
			}
			if j > 0 {
				script.WriteString(", ")
			}
			script.WriteString(literal)
		}
		script.WriteString(");\n")
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("没有可导出的行")
	}
	return script.String(), nil
}
