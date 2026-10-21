package main

// Unit tests for the batch-3 cluster: structure DDL generation, table data
// cloning for pins, and Quick Look helpers.

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/widget"
)

// --- structure DDL ----------------------------------------------------------

func TestBuildAlterDDLMysql(t *testing.T) {
	edits := []*colEdit{
		{name: "a", oldType: "INT", nullable: false},         // unchanged
		{name: "b", oldType: "VARCHAR(10)", nullable: false}, // type change
		{name: "c", oldType: "TEXT", nullable: true},         // drop
	}
	edits[1].typeEntry = newEntryForTest("VARCHAR(255)")
	edits[1].nullChk = newCheckForTest(false)
	edits[2].dropChk = newCheckForTest(true)
	// no changes on edits[0]
	edits[0].typeEntry = newEntryForTest("INT")
	edits[0].nullChk = newCheckForTest(false)

	newEdit := &colEdit{isNew: true}
	newEdit.nameEntry = newEntryForTest("d")
	newEdit.typeEntry = newEntryForTest("INT")
	newEdit.nullChk = newCheckForTest(true)

	q := func(s string) string { return s }
	stmts := buildAlterDDL("mysql", "users", q, edits, newEdit)

	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements (modify b, drop c, add d), got %d: %q", len(stmts), stmts)
	}
	joined := strings.Join(stmts, " | ")
	for _, want := range []string{
		"MODIFY COLUMN b VARCHAR(255)",
		"DROP COLUMN c",
		"ADD COLUMN d INT",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestBuildAlterDDLNoChange(t *testing.T) {
	e := &colEdit{name: "a", oldType: "INT", nullable: true}
	e.typeEntry = newEntryForTest("INT")
	e.nullChk = newCheckForTest(true)
	newEdit := &colEdit{isNew: true}
	newEdit.nameEntry = newEntryForTest("")
	newEdit.typeEntry = newEntryForTest("")
	newEdit.nullChk = newCheckForTest(true)

	stmts := buildAlterDDL("mysql", "users", func(s string) string { return s }, []*colEdit{e}, newEdit)
	if len(stmts) != 0 {
		t.Fatalf("expected no statements, got %q", stmts)
	}
}

// --- pin snapshot clone -------------------------------------------------------

func TestCloneTableDataDeep(t *testing.T) {
	v1 := "x"
	orig := &TableData{Cols: []ColumnInfo{{Name: "a"}}, Rows: [][]*string{{&v1}}}
	clone := cloneTableData(orig)
	if len(clone.Rows) != 1 || *clone.Rows[0][0] != "x" {
		t.Fatal("clone lost data")
	}
	// Mutating the clone must not affect the original.
	clone.Rows[0][0] = nil
	if orig.Rows[0][0] == nil {
		t.Fatal("clone shares row slices with original")
	}
	clone.Cols[0].Name = "zzz"
	if orig.Cols[0].Name != "a" {
		t.Fatal("clone shares column slice with original")
	}
}

// --- quick look helpers --------------------------------------------------------

func TestLooksBinary(t *testing.T) {
	if !looksBinary("abc\x00def") {
		t.Error("NUL byte should be binary")
	}
	if !looksBinary(string([]byte{0xff, 0xfe, 0xfd})) {
		t.Error("invalid UTF-8 should be binary")
	}
	if looksBinary("plain text") {
		t.Error("plain text must not be binary")
	}
	if looksBinary("") {
		t.Error("empty must not be binary")
	}
}

func TestHexViewLabel(t *testing.T) {
	l := hexViewLabel("AB")
	text := l.Text
	if !strings.Contains(text, "0000") || !strings.Contains(text, "41") || !strings.Contains(text, "42") {
		t.Errorf("hex dump missing offset/values: %q", text)
	}
	if !strings.Contains(text, "AB") { // ascii column
		t.Errorf("hex dump missing ascii column: %q", text)
	}
}

// tiny widget factories for the DDL builder tests (headless-safe).
func newEntryForTest(s string) *widget.Entry {
	e := widget.NewEntry()
	e.SetText(s)
	return e
}

func newCheckForTest(checked bool) *widget.Check {
	c := widget.NewCheck("", func(bool) {})
	c.SetChecked(checked)
	return c
}
