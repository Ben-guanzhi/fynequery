package db

// Unit tests for the transactional SQL builders shared by the single-row
// UpdateRow/DeleteRow/InsertRow and the batch ApplyChanges path. These are
// pure functions (no DB required), so they run in plain `go test`.
//
// The builders iterate over Go maps, so column order in the generated SQL is
// non-deterministic. The tests therefore compare the SQL as sets of clauses
// (assignment/WHERE terms) rather than exact strings.

import (
	"reflect"
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }

// splitClauses splits a comma-separated SQL fragment into its terms.
func splitClauses(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// hasAll reports whether every want element appears in got (order-independent).
func hasAll(got, want []string) bool {
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// argsSet compares args as a multiset (order-independent, handles NULL).
func argsSet(got, want []interface{}) bool {
	if len(got) != len(want) {
		return false
	}
	used := make([]bool, len(got))
	for _, w := range want {
		matched := false
		for i, g := range got {
			if used[i] {
				continue
			}
			if g == nil && w == nil {
				used[i], matched = true, true
				break
			}
			if g != nil && w != nil && g == w {
				used[i], matched = true, true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func TestBuildUpdateDefaultSentinel(t *testing.T) {
	pk := map[string]*string{"id": strPtr("7")}
	changes := map[string]*string{
		"name":   strPtr("bob"),
		"age":    strPtr(DefaultMarker), // writes DEFAULT
		"active": nil,                   // writes NULL
	}
	q, args := buildUpdate("users", pk, changes)

	// UPDATE `users` SET <a>, <b>, <c> WHERE <cond>
	body := strings.TrimPrefix(q, "UPDATE `users` SET ")
	setPart, wherePart, ok := strings.Cut(body, " WHERE ")
	if !ok {
		t.Fatalf("malformed SQL: %s", q)
	}
	sets := splitClauses(setPart)
	if !hasAll(sets, []string{"`name` = ?", "`age` = DEFAULT", "`active` = ?"}) || len(sets) != 3 {
		t.Fatalf("SET clauses = %v", sets)
	}
	if wherePart != "`id` = ?" {
		t.Fatalf("WHERE = %q", wherePart)
	}
	if !argsSet(args, []interface{}{"bob", nil, "7"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildUpdateNormalValues(t *testing.T) {
	pk := map[string]*string{"id": strPtr("1"), "kind": strPtr("a")}
	changes := map[string]*string{"note": strPtr("hi")}
	q, args := buildUpdate("t", pk, changes)

	body := strings.TrimPrefix(q, "UPDATE `t` SET ")
	setPart, wherePart, ok := strings.Cut(body, " WHERE ")
	if !ok {
		t.Fatalf("malformed SQL: %s", q)
	}
	if setPart != "`note` = ?" {
		t.Fatalf("SET = %q", setPart)
	}
	// WHERE conditions are AND-joined.
	conds := strings.Split(wherePart, " AND ")
	if !hasAll(conds, []string{"`id` = ?", "`kind` = ?"}) || len(conds) != 2 {
		t.Fatalf("WHERE = %q", wherePart)
	}
	if !argsSet(args, []interface{}{"hi", "1", "a"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildDelete(t *testing.T) {
	pk := map[string]*string{"id": strPtr("9")}
	q, args := buildDelete("orders", pk)
	if q != "DELETE FROM `orders` WHERE `id` = ?" {
		t.Fatalf("SQL = %q", q)
	}
	if !reflect.DeepEqual(args, []interface{}{"9"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildInsertOmitsDefaultColumns(t *testing.T) {
	values := map[string]*string{
		"id":      strPtr(DefaultMarker), // omit → server default
		"name":    strPtr("ann"),
		"created": nil, // explicit NULL
	}
	q, args, err := buildInsert("users", values)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// INSERT INTO `users` (`a`, `b`) VALUES (?, ?)
	rest := strings.TrimPrefix(q, "INSERT INTO `users` (")
	colPart, valPart, ok := strings.Cut(rest, ") VALUES (")
	if !ok {
		t.Fatalf("malformed SQL: %s", q)
	}
	valPart = strings.TrimSuffix(valPart, ")")
	if !hasAll(splitClauses(colPart), []string{"`name`", "`created`"}) || len(splitClauses(colPart)) != 2 {
		t.Fatalf("columns = %v", splitClauses(colPart))
	}
	if valPart != "?, ?" {
		t.Fatalf("placeholders = %q", valPart)
	}
	if !argsSet(args, []interface{}{"ann", nil}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildInsertNoValues(t *testing.T) {
	// A DEFAULT-only insert has no explicit column, which the builder rejects.
	values := map[string]*string{"id": strPtr(DefaultMarker), "ts": strPtr(DefaultMarker)}
	if _, _, err := buildInsert("t", values); err == nil {
		t.Fatal("expected error for DEFAULT-only insert")
	}
	values = map[string]*string{}
	if _, _, err := buildInsert("t", values); err == nil {
		t.Fatal("expected error for empty insert")
	}
}
