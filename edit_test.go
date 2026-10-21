package main

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

func TestEditableGrid(t *testing.T) {
	test.NewApp()
	s := func(v string) *string { return &v }
	data := &TableData{
		Cols: []ColumnInfo{
			{Name: "id", Type: "int", Key: "PK"},
			{Name: "name", Type: "varchar"},
		},
		Rows:    [][]*string{{s("1"), s("a")}, {s("2"), s("b")}},
		Fetched: 2, Total: 2,
	}
	g := newDataTable(data, newStatusBar())
	if !g.hasPK() {
		t.Fatal("expected a primary key")
	}
	g.setEditable(true)
	if !g.editable {
		t.Fatal("editable not enabled")
	}
	g.setCell(0, 1, s("A"))
	g.selRow = 1
	if !g.markSelectedDeleted() {
		t.Fatal("markSelectedDeleted failed")
	}
	if got := g.changeCount(); got != 2 {
		t.Fatalf("changeCount = %d, want 2", got)
	}
	ups, dels := g.pending()
	if len(ups) != 1 || len(dels) != 1 {
		t.Fatalf("pending: %d updates, %d deletes", len(ups), len(dels))
	}
	if *ups[0].pk["id"] != "1" || *ups[0].changes["name"] != "A" {
		t.Fatalf("update = %+v", ups[0])
	}
	if *dels[0]["id"] != "2" {
		t.Fatalf("delete = %+v", dels[0])
	}

	// A table without a primary key cannot be edited.
	g2 := newDataTable(&TableData{
		Cols: []ColumnInfo{{Name: "x", Type: "int"}},
		Rows: [][]*string{{s("1")}},
	}, newStatusBar())
	g2.setEditable(true)
	if g2.editable {
		t.Fatal("editable should stay off without a PK")
	}
}
