package main

import (
	"strings"
	"testing"
)

func TestFormatBasicSelect(t *testing.T) {
	in := "select id,name from users where x=1"
	out := formatSQL(in)
	want := "SELECT id, name\nFROM users\nWHERE x = 1"
	if out != want {
		t.Fatalf("basic select:\n got: %q\nwant: %q", out, want)
	}
}

func TestFormatUppercasesKeywords(t *testing.T) {
	out := formatSQL("select a from t where b in (1,2) and c is not null")
	if !strings.Contains(out, "SELECT a") || !strings.Contains(out, "IS NOT NULL") {
		t.Fatalf("keywords not uppercased: %q", out)
	}
}

func TestFormatPreservesStringLiteral(t *testing.T) {
	out := formatSQL("select * from t where name='Bob Smith' and active=1")
	if !strings.Contains(out, "'Bob Smith'") {
		t.Fatalf("string literal mangled: %q", out)
	}
	if strings.Contains(out, "BOB SMITH") {
		t.Fatalf("string content uppercased: %q", out)
	}
	if !strings.Contains(out, "AND active = 1") {
		t.Fatalf("AND not indented/claused: %q", out)
	}
}

func TestFormatPreservesComment(t *testing.T) {
	out := formatSQL("select 1 -- pick a number\nfrom dual")
	if !strings.Contains(out, "-- pick a number") {
		t.Fatalf("comment dropped: %q", out)
	}
	if !strings.Contains(out, "SELECT 1") {
		t.Fatalf("comment broke statement: %q", out)
	}
}

func TestFormatJoin(t *testing.T) {
	out := formatSQL("select a,b from x left join y on x.id=y.id")
	if !strings.Contains(out, "FROM x") {
		t.Fatalf("missing FROM x: %q", out)
	}
	if !strings.Contains(out, "LEFT JOIN y") {
		t.Fatalf("missing LEFT JOIN y: %q", out)
	}
	if !strings.Contains(out, "ON x.id = y.id") {
		t.Fatalf("missing ON clause: %q", out)
	}
}

func TestFormatMultipleStatements(t *testing.T) {
	out := formatSQL("select 1; select 2")
	if !strings.Contains(out, "SELECT 1;") {
		t.Fatalf("first statement lost: %q", out)
	}
	if !strings.Contains(out, "SELECT 2") {
		t.Fatalf("second statement lost: %q", out)
	}
}

func TestFormatIndentsAndOr(t *testing.T) {
	out := formatSQL("select a from t where x=1 and y=2 or z=3")
	lines := strings.Split(out, "\n")
	foundAnd, foundOr := false, false
	for _, l := range lines {
		if strings.HasPrefix(l, "  AND") {
			foundAnd = true
		}
		if strings.HasPrefix(l, "  OR") {
			foundOr = true
		}
	}
	if !foundAnd || !foundOr {
		t.Fatalf("AND/OR not indented: %q", out)
	}
}

func TestFormatKeepsIdentifierCase(t *testing.T) {
	out := formatSQL("select userName from Users")
	if !strings.Contains(out, "userName") || !strings.Contains(out, "Users") {
		t.Fatalf("identifier case changed: %q", out)
	}
}
