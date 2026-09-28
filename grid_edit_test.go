package main

import (
	"testing"

	"tablepro-fyne/internal/db"
)

func TestCollectChangesOrderAndNULL(t *testing.T) {
	c := &Client{
		gridCols: []string{"id", "name"},
		lastRows: [][]interface{}{
			{1.0, "a"},
			{2.0, "b"},
			{3.0, "c"},
		},
		edits: newGridEditState(),
	}
	c.edits.setUpdate(2, "name", "x")
	c.edits.setUpdate(0, "name", "") // NULL
	c.edits.deletes[0] = true
	c.edits.inserts = append(c.edits.inserts, map[string]string{"name": "n", "id": "9"})

	changes := c.collectChanges()
	// Queue order: updates by row, deletes by row, then new rows.
	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3", len(changes))
	}
	if changes[0].Kind != db.RowUpdate || changes[0].Original[0] != 3.0 {
		t.Errorf("first change = %+v, want update of row 2", changes[0])
	}
	if changes[1].Kind != db.RowDelete || changes[1].Original[0] != 1.0 {
		t.Errorf("second change = %+v, want delete of row 0", changes[1])
	}
	if changes[2].Kind != db.RowInsert {
		t.Errorf("third change = %+v, want insert", changes[2])
	}
	ins := db.RowChange{Kind: db.RowInsert, Edits: c.editsOf(c.edits.inserts[0])}
	statements, err := db.BuildStatements("mysql", "`t`", c.gridCols, []string{"id"},
		map[string]string{"id": "int", "name": "varchar(50)", "amount": "decimal(18,2)"}, []db.RowChange{ins})
	if err != nil {
		t.Fatalf("insert build: %v", err)
	}
	if statements[0] != "INSERT INTO `t` (`id`, `name`) VALUES (9, 'n')" {
		t.Errorf("unexpected insert: %s", statements[0])
	}
}
