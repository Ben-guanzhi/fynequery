package db

import (
	"strings"
	"testing"
)

func BenchmarkInsertScript(b *testing.B) {
	cols := []string{"id", "name", "note", "score", "city"}
	types := map[string]string{
		"id": "integer", "name": "varchar(50)", "note": "text",
		"score": "decimal(10,2)", "city": "nvarchar(30)",
	}
	rows := make([][]interface{}, insertExportMaxRows)
	for i := range rows {
		rows[i] = []interface{}{float64(i), "customer", "It's here", "12.50", "上海浦东"}
		if i%3 == 0 {
			rows[i][4] = nil
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := InsertScript("sqlite", `"parts"`, "parts", cols, types, rows)
		if err != nil {
			b.Fatal(err)
		}
		if !strings.HasPrefix(got, "INSERT INTO") {
			b.Fatal("empty script")
		}
	}
}

// BenchmarkInsertScriptOld runs the pre-builder renderer (kept in the test
// file as the equivalence oracle) over the same rows in the same process.
func BenchmarkInsertScriptOld(b *testing.B) {
	cols := []string{"id", "name", "note", "score", "city"}
	types := map[string]string{
		"id": "integer", "name": "varchar(50)", "note": "text",
		"score": "decimal(10,2)", "city": "nvarchar(30)",
	}
	rows := make([][]interface{}, insertExportMaxRows)
	for i := range rows {
		rows[i] = []interface{}{float64(i), "customer", "It's here", "12.50", "上海浦东"}
		if i%3 == 0 {
			rows[i][4] = nil
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := oldInsertScript("sqlite", `"parts"`, "parts", cols, types, rows)
		if err != nil {
			b.Fatal(err)
		}
		if !strings.HasPrefix(got, "INSERT INTO") {
			b.Fatal("empty script")
		}
	}
}
