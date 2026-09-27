package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

// Tests for the pure rendering and extraction primitives: the CSV/JSON
// writers, the statement-table extractor and the database filter masks.

func TestWriteCSVRoundTrip(t *testing.T) {
	header := []string{"id", "note", "note (varchar)"}
	rows := [][]string{
		{"1", "plain", "has, comma"},
		{"2", "has \"quote\"", "line\nbreak"},
	}
	var buf bytes.Buffer
	if err := writeCSV(&buf, header, rows); err != nil {
		t.Fatalf("writeCSV: %v", err)
	}

	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("rows = %d, want 3", len(records))
	}
	// Export strips the "(TYPE)" suffix the grid header appends.
	if records[0][2] != "note" {
		t.Errorf("exported header = %v, want plain column names", records[0])
	}
	if records[1][2] != "has, comma" || records[2][1] != `has "quote"` || records[2][2] != "line\nbreak" {
		t.Errorf("values not round-tripped: %v", records)
	}
}

func TestWriteJSONDedupesColumns(t *testing.T) {
	header := []string{"id", "id (int)", "name"}
	rows := [][]string{{"1", "2", "x"}}
	var buf bytes.Buffer
	if err := writeJSON(&buf, header, rows); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}

	var objects []map[string]string
	if err := json.Unmarshal(buf.Bytes(), &objects); err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if len(objects) != 1 {
		t.Fatalf("objects = %d, want 1", len(objects))
	}
	if objects[0]["id"] != "1" || objects[0]["id_2"] != "2" || objects[0]["name"] != "x" {
		t.Errorf("json object = %v", objects[0])
	}
}

func TestStatementTable(t *testing.T) {
	cases := []struct{ sql, want string }{
		{"SELECT * FROM users", "users"},
		{"select * from dbo.orders", "orders"},
		{"UPDATE t SET a = 1", "t"},
		{"INSERT INTO logs (a) VALUES (1)", "logs"},
		{"SELECT * FROM `mydb`.`t`", "t"},
		{"SELECT * FROM [srv].[db].[t]", "t"},
		{"SELECT * FROM -- which?\nusers", "users"},
		{"SELECT 'from decoy' FROM real_t", "real_t"},
		{"SELECT * FROM t WHERE a = 'from decoy'", "t"},
		{"DELETE FROM t", "t"},
		{"SELECT 1", ""},
		{"SELECT * FROM 123", ""},        // not identifier-like
		{"SELECT * FROM \"weird;\"", ""}, // non-identifier characters
	}
	for _, tc := range cases {
		if got := statementTable(tc.sql); got != tc.want {
			t.Errorf("statementTable(%q) = %q, want %q", tc.sql, got, tc.want)
		}
	}
}

func TestMatchMask(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"sys%", "sysdatabases", true},
		{"sys%", "master", false},
		{"%", "anything", true},
		{"a_b", "axb", true},
		{"a_b", "ab", false},
		{"a.b", "axb", false}, // a dot is a literal dot, not a wildcard
		{"AB%", "abc", true},  // masks are case-insensitive
		{"exact", "exact", true},
		{"", "anything", false},
	}
	for _, tc := range cases {
		f := DatabaseFilter{Include: []string{tc.pattern}}
		if got := f.allows(tc.name); got != tc.want {
			t.Errorf("mask %q vs %q = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
	// Exclude wins over nothing to include; include list is an allowlist.
	f := DatabaseFilter{Exclude: []string{"tmp%"}}
	if !f.allows("users") || f.allows("tmp_log") {
		t.Error("exclude-only filter misbehaves")
	}
	f = DatabaseFilter{Include: []string{"a%"}, Exclude: []string{"ab%"}}
	if f.allows("abc") {
		t.Error("exclude must win over include")
	}
	if !f.allows("aaa") {
		t.Error("include match should pass")
	}
}

func TestStripTypeSuffix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"name (varchar)", "name"},
		{"name", "name"},
		{"count (int)", "count"},
		{"(x)", "(x)"},         // no " (" separator: nothing to strip
		{"a (b) (c)", "a (b)"}, // strips the last suffix only
	}
	for _, tc := range cases {
		if got := stripTypeSuffix(tc.in); got != tc.want {
			t.Errorf("stripTypeSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := exportColumns([]string{"id", "name (varchar)"}); len(got) != 2 || got[0] != "id" || got[1] != "name" {
		t.Errorf("exportColumns = %v", got)
	}
	if !strings.Contains(strings.Join(nil, ""), "") {
		t.Error("sanity") // keep the strings import honest if cases shrink
	}
}
