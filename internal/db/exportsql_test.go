package db

import (
	"fmt"
	"strings"
	"testing"
)

// oldInsertScript is the pre-builder renderer kept verbatim as the oracle for
// the optimized one: join the column list per row, Sprintf one statement per
// row, Join the statements and append the trailing newline. Byte-for-byte
// equality with this is the whole contract of the strings.Builder rewrite,
// and it mirrors the root client's exportsql_test.go oracle.
func oldInsertScript(
	dbType string, name string, table string,
	resultColumns []string, types map[string]string, rows [][]interface{},
) (string, error) {
	idents := make([]string, len(resultColumns))
	colTypes := make([]string, len(resultColumns))
	for i, col := range resultColumns {
		colType, ok := types[strings.ToLower(col)]
		if !ok {
			return "", fmt.Errorf("结果列 %s 不是表 %s 的字段", col, table)
		}
		ident, err := quoteIdent(dbType, col)
		if err != nil {
			return "", err
		}
		idents[i] = ident
		colTypes[i] = colType
	}
	statements := make([]string, 0, len(rows))
	for _, row := range rows {
		values := make([]string, len(row))
		for j, value := range row {
			literal, err := valueLiteral(dbType, colTypes[j], value)
			if err != nil {
				return "", err
			}
			values[j] = literal
		}
		statements = append(statements, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);",
			name, strings.Join(idents, ", "), strings.Join(values, ", ")))
	}
	if len(statements) == 0 {
		return "", fmt.Errorf("没有可导出的行")
	}
	return strings.Join(statements, "\n") + "\n", nil
}

func TestInsertScriptBuilderMatchesTheOldRendererByteForByte(t *testing.T) {
	cols := []string{"id", "name", "note", "score", "city"}
	types := map[string]string{
		"id": "integer", "name": "varchar(50)", "note": "text",
		"score": "decimal(10,2)", "city": "nvarchar(30)",
	}
	rows := [][]interface{}{
		{float64(1), "O'Brien", nil, "12.50", "上海"},
		{"2", "", "多行\n文本", "0", nil},
		{float64(3), "back\\slash", "a'b'c", "-1.25", "x"},
	}
	for _, dbType := range []string{"sqlite", "mysql", "postgresql", "sqlserver", "oracle"} {
		got, err := InsertScript(dbType, `"parts"`, "parts", cols, types, rows)
		want, wantErr := oldInsertScript(dbType, `"parts"`, "parts", cols, types, rows)
		if (err == nil) != (wantErr == nil) || got != want {
			t.Errorf("%s: got %q (%v), want %q (%v)", dbType, got, err, want, wantErr)
		}
		if _, err := InsertScript(dbType, `"parts"`, "parts", cols, types, nil); err == nil ||
			!strings.Contains(err.Error(), "没有可导出的行") {
			t.Errorf("%s: empty result: want the old refusal, got %v", dbType, err)
		}
	}
}

// The mechanism, not just the output: the old loop allocated the row's values
// slice, two Joins and one Sprintf per row - about a dozen allocations per row
// against the builder's literal-only six. Same bound as the root's test.
func TestInsertScriptAllocatesOnlyTheLiteralsPerRow(t *testing.T) {
	const rowCount = 1000
	cols := []string{"id", "name", "note", "score", "city"}
	types := map[string]string{
		"id": "integer", "name": "varchar(50)", "note": "text",
		"score": "decimal(10,2)", "city": "nvarchar(30)",
	}
	rows := make([][]interface{}, rowCount)
	for i := range rows {
		rows[i] = []interface{}{float64(i), "customer", "It's here", "12.50", "上海浦东"}
		if i%3 == 0 {
			rows[i][4] = nil
		}
	}
	allocs := testing.AllocsPerRun(20, func() {
		if _, err := InsertScript("sqlite", `"parts"`, "parts", cols, types, rows); err != nil {
			t.Fatal(err)
		}
	})
	// Five cells a row render with at most one string each - measured 5.7
	// allocations per row - plus growth headroom for the builder itself. The
	// old loop needed about 12.7 per row (values slice, two joins, one
	// Sprintf); a per-row join creeping back alone also crosses this line.
	if ceiling := float64(rowCount*6 + 64); allocs > ceiling {
		t.Errorf("InsertScript allocated %v times for %d rows, ceiling %v: "+
			"the column list must be joined once per export, not once per row", allocs, rowCount, ceiling)
	}
}
