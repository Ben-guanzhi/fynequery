package main

// Cell values the grid can hold but the one-line box could not show: a value
// that already has line breaks, and NULL as distinct from an empty string.

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// TestMultilineValueDecidesWhichBoxToShow: line breaks always get the taller
// box, and so does a value too wide to read across one line. A short value
// keeps Enter's meaning inside a form, which is to submit it.
func TestMultilineValueDecidesWhichBoxToShow(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"alice", false},
		{"", false},
		{"a\nb", true},
		{"a\r\nb", true},
		{strings.Repeat("x", 80), false},
		{strings.Repeat("x", 81), true},
		{strings.Repeat("汉", 41), true},  // 41 wide runes = 82 columns
		{strings.Repeat("汉", 40), false}, // 80 columns
	}
	for _, tc := range cases {
		if got := multilineValue(tc.value); got != tc.want {
			t.Errorf("multilineValue(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// TestAssignCellQueuesANull is what the details pane's 设为 NULL does: queue
// the edit without a dialog, then report it. The queue stores strings where ""
// means NULL, so a column can be set to NULL - but not to an empty string.
func TestAssignCellQueuesANull(t *testing.T) {
	c, _ := newGridClient(t)
	c.grid.src = db.TableRef{Database: "db", Schema: "", Table: "t"}

	c.assignCell(detailsField{Name: "name", Col: 1}, 0)

	changes := c.collectChanges()
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want one row change", changes)
	}
	if len(changes[0].Edits) != 1 || changes[0].Edits[0].Column != "name" || changes[0].Edits[0].Value != nil {
		t.Fatalf("queued edits = %+v, want name = NULL", changes[0].Edits)
	}
	if !strings.Contains(c.status.Text, "NULL") {
		t.Errorf("status = %q, want it to say NULL", c.status.Text)
	}
}

// TestSetCellValueRefusesAReadOnlyResult: without a source table there is no
// table name to render, and inventing one is how an INSERT lands somewhere the
// user never chose.
func TestSetCellValueRefusesAReadOnlyResult(t *testing.T) {
	c, _ := newGridClient(t) // src left zero: the result is read-only

	if err := c.setCellValue(0, 0, "x"); err == "" {
		t.Fatal("a read-only result accepted an edit")
	}
}

// TestSetCellValueRefusesADeletedRow covers the other silent-loss case: a row
// queued for DELETE is dropped from the result, so an UPDATE aimed at it can
// only ever fail - or, worse, point at whatever moves into its place.
func TestSetCellValueRefusesADeletedRow(t *testing.T) {
	c, _ := newGridClient(t)
	c.grid.src = db.TableRef{Database: "db", Schema: "", Table: "t"}
	c.grid.edits.deletes[0] = true

	if err := c.setCellValue(0, 0, "x"); err == "" {
		t.Fatal("a deleted row accepted an edit")
	}
}

// TestCopyResultNeedsAResult: an empty grid has nothing to put on the clipboard
// and must say so rather than copying an empty string.
func TestCopyResultNeedsAResult(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildUI())
	a.Clipboard().SetContent("untouched")

	c.copyResultAs("csv")

	if got := a.Clipboard().Content(); got != "untouched" {
		t.Errorf("clipboard = %q, want it left alone", got)
	}
	if !strings.Contains(c.status.Text, "没有可复制的结果") {
		t.Errorf("status = %q, want the reason nothing was copied", c.status.Text)
	}
}
