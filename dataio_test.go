package main

// Tests for the data import parser (CSV / JSON → rows keyed by header). The
// parser is pure (no session/UI), so it is exercised directly.

import (
	"reflect"
	"testing"
)

func strp(s string) *string { return &s }

func TestParseImportCSV(t *testing.T) {
	data := []byte("id,name,note\n1,alice,\n2,bob,hi\n")
	rows, headers, err := parseImport(data, "people.csv")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(headers, []string{"id", "name", "note"}) {
		t.Fatalf("headers = %v", headers)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if *rows[0]["id"] != "1" || *rows[0]["name"] != "alice" {
		t.Fatalf("row0 = %+v", rows[0])
	}
	// An empty CSV field is NULL.
	if rows[0]["note"] != nil {
		t.Fatalf("empty field should be NULL, got %v", rows[0]["note"])
	}
	if *rows[1]["note"] != "hi" {
		t.Fatalf("row1 note = %v", rows[1]["note"])
	}
}

func TestParseImportCSVRagged(t *testing.T) {
	// A short row must not panic; missing trailing fields are simply absent.
	data := []byte("a,b,c\n1,2\n")
	rows, _, err := parseImport(data, "x.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || *rows[0]["a"] != "1" || *rows[0]["b"] != "2" {
		t.Fatalf("ragged row = %+v", rows)
	}
	if _, ok := rows[0]["c"]; ok {
		t.Fatal("missing field should be absent, not present")
	}
}

func TestParseImportJSON(t *testing.T) {
	data := []byte(`[{"id":1,"name":"alice","note":null},{"id":2,"name":"bob","active":true}]`)
	rows, headers, err := parseImport(data, "people.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if *rows[0]["id"] != "1" {
		t.Fatalf("json number id = %v", rows[0]["id"])
	}
	if rows[0]["note"] != nil {
		t.Fatalf("json null should be NULL, got %v", rows[0]["note"])
	}
	if *rows[1]["active"] != "true" {
		t.Fatalf("json bool = %v", rows[1]["active"])
	}
	// Headers are the union of keys, sorted.
	want := []string{"active", "id", "name", "note"}
	if !reflect.DeepEqual(headers, want) {
		t.Fatalf("headers = %v, want %v", headers, want)
	}
}

func TestParseImportJSONByContent(t *testing.T) {
	// A .txt name but JSON content is still detected as JSON.
	data := []byte(`[{"k":"v"}]`)
	rows, _, err := parseImport(data, "data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || *rows[0]["k"] != "v" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestParseImportErrors(t *testing.T) {
	if _, _, err := parseImport([]byte("not,valid\n\"unterminated"), "x.csv"); err == nil {
		t.Fatal("expected CSV parse error")
	}
	if _, _, err := parseImport([]byte("[{bad json}]"), "x.json"); err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestAllJSON(t *testing.T) {
	tbl := &dataTable{data: &TableData{
		Cols: []ColumnInfo{{Name: "id"}, {Name: "name"}},
		Rows: [][]*string{
			{strp("1"), strp("alice")},
			{strp("2"), nil}, // NULL name
		},
	}}
	got := tbl.allJSON()
	want := `[
  {
    "id": "1",
    "name": "alice"
  },
  {
    "id": "2",
    "name": null
  }
]`
	if got != want {
		t.Fatalf("allJSON mismatch:\n got=%s\nwant=%s", got, want)
	}
}
