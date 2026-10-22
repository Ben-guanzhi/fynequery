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
	ups, dels, ins := g.pending()
	if len(ups) != 1 || len(dels) != 1 || len(ins) != 0 {
		t.Fatalf("pending: %d updates, %d deletes, %d inserts", len(ups), len(dels), len(ins))
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

func TestStageInsert(t *testing.T) {
	test.NewApp()
	s := func(v string) *string { return &v }
	data := &TableData{
		Cols: []ColumnInfo{
			{Name: "id", Type: "int", Key: "PK"},
			{Name: "name", Type: "varchar"},
		},
		Rows:    [][]*string{{s("1"), s("a")}},
		Fetched: 1, Total: 1,
	}
	g := newDataTable(data, newStatusBar())
	g.setEditable(true)

	idx := g.stageInsert(map[string]*string{"name": s("new")})
	if idx != 1 {
		t.Fatalf("stageInsert index = %d, want 1", idx)
	}
	if len(g.data.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(g.data.Rows))
	}
	if got := g.changeCount(); got != 1 {
		t.Fatalf("changeCount = %d, want 1", got)
	}
	ups, dels, ins := g.pending()
	if len(ups) != 0 || len(dels) != 0 || len(ins) != 1 {
		t.Fatalf("pending: %d updates, %d deletes, %d inserts", len(ups), len(dels), len(ins))
	}
	if ins[0]["name"] == nil || *ins[0]["name"] != "new" {
		t.Fatalf("insert values = %+v", ins[0])
	}
	if _, ok := ins[0]["id"]; ok {
		t.Fatalf("unset PK column must be omitted from the insert: %+v", ins[0])
	}

	// Rollback (clearDirty) removes the staged row from the view.
	g.clearDirty()
	if len(g.data.Rows) != 1 {
		t.Fatalf("after clearDirty rows = %d, want 1", len(g.data.Rows))
	}
	if got := g.changeCount(); got != 0 {
		t.Fatalf("changeCount after clearDirty = %d, want 0", got)
	}
}
