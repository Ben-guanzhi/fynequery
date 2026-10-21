package main

import (
	"testing"

	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// newUndoTestClient is a bare client holding a writable two-row result: the
// minimum setEditContext would produce, minus the widgets afterEditQueued
// touches (all nil-guarded), so the undo/redo action path can run headless.
func newUndoTestClient() *Client {
	return &Client{
		grid: gridState{
			cols: []string{"id", "name"},
			rows: [][]string{{"1", "a"}, {"2", "b"}},
			lastRows: [][]interface{}{
				{1.0, "a"},
				{2.0, "b"},
			},
			edits: newGridEditState(),
			src:   db.TableRef{Table: "t"},
		},
	}
}

// TestUndoRedoCellEdit is the core promise: an edit is staged in memory, Ctrl+Z
// takes it back, Ctrl+Shift+Z puts it back. No database is involved.
func TestUndoRedoCellEdit(t *testing.T) {
	c := newUndoTestClient()
	if err := c.setCellValue(0, 1, "z"); err != "" {
		t.Fatalf("setCellValue: %v", err)
	}
	if v, ok := c.grid.edits.updateAt(0, "name"); !ok || v != "z" {
		t.Fatalf("edit not queued: %q ok=%v", v, ok)
	}
	if len(c.grid.undoStack) != 1 {
		t.Fatalf("undo stack = %d, want 1", len(c.grid.undoStack))
	}

	c.undoGridEdit()
	if _, ok := c.grid.edits.updateAt(0, "name"); ok {
		t.Error("undo left the edit in place")
	}
	if len(c.grid.redoStack) != 1 {
		t.Fatalf("redo stack = %d, want 1", len(c.grid.redoStack))
	}

	c.redoGridEdit()
	if v, ok := c.grid.edits.updateAt(0, "name"); !ok || v != "z" {
		t.Errorf("redo did not restore the edit: %q ok=%v", v, ok)
	}
}

// TestUndoNewRowCellEdit covers the other branch of setCellValue - an edit on a
// queued new row - so undo reaches into the insert's map too.
func TestUndoNewRowCellEdit(t *testing.T) {
	c := newUndoTestClient()
	c.insertRow() // one queued new row at index 0 (row index 2 on screen)
	if err := c.setCellValue(2, 1, "n"); err != "" {
		t.Fatalf("setCellValue: %v", err)
	}
	if got := c.grid.edits.inserts[0]["name"]; got != "n" {
		t.Fatalf("insert edit not queued: %q", got)
	}

	c.undoGridEdit() // undoes the cell edit, keeps the row
	if _, ok := c.grid.edits.inserts[0]["name"]; ok {
		t.Error("undo left the new-row edit in place")
	}
	if len(c.grid.edits.inserts) != 1 {
		t.Error("undo removed the new row instead of just the cell edit")
	}
}

// TestUndoRestoresDeletedRow: a row marked for deletion comes back on undo.
func TestUndoRestoresDeletedRow(t *testing.T) {
	c := newUndoTestClient()
	c.grid.hasCell = true
	c.grid.cell = widget.TableCellID{Row: 1, Col: 0}
	c.deleteSelectedRow()
	if !c.grid.edits.isDeleted(1) {
		t.Fatal("row not marked deleted")
	}
	c.undoGridEdit()
	if c.grid.edits.isDeleted(1) {
		t.Error("undo did not restore the deleted row")
	}
	c.redoGridEdit()
	if !c.grid.edits.isDeleted(1) {
		t.Error("redo did not re-mark the row deleted")
	}
}

// TestUndoRemovesInsertedRow: a queued new row disappears on undo and returns on
// redo.
func TestUndoRemovesInsertedRow(t *testing.T) {
	c := newUndoTestClient()
	c.insertRow()
	if len(c.grid.edits.inserts) != 1 {
		t.Fatalf("inserts = %d, want 1", len(c.grid.edits.inserts))
	}
	c.undoGridEdit()
	if len(c.grid.edits.inserts) != 0 {
		t.Error("undo did not remove the inserted row")
	}
	c.redoGridEdit()
	if len(c.grid.edits.inserts) != 1 {
		t.Error("redo did not restore the inserted row")
	}
}

// TestUndoThenEditClearsRedo is standard undo semantics: a fresh edit after an
// undo invalidates the redo branch.
func TestUndoThenEditClearsRedo(t *testing.T) {
	c := newUndoTestClient()
	c.setCellValue(0, 1, "z")
	c.undoGridEdit()
	if len(c.grid.redoStack) != 1 {
		t.Fatalf("redo stack = %d, want 1 before the fresh edit", len(c.grid.redoStack))
	}
	c.setCellValue(1, 1, "y") // a new edit
	if len(c.grid.redoStack) != 0 {
		t.Error("a fresh edit did not clear the redo stack")
	}
}

// TestUndoEmptyStackIsSafe: undo/redo with nothing to do must not panic.
func TestUndoEmptyStackIsSafe(t *testing.T) {
	c := newUndoTestClient()
	c.undoGridEdit()
	c.redoGridEdit()
}

// TestUndoDiscardRestoresQueue: 放弃 drops the whole queue but is itself
// undoable, so a mis-click is recoverable.
func TestUndoDiscardRestoresQueue(t *testing.T) {
	c := newUndoTestClient()
	c.setCellValue(0, 1, "z")
	c.discardChanges()
	if c.grid.edits.any() {
		t.Fatal("discard did not clear the queue")
	}
	c.undoGridEdit()
	if v, ok := c.grid.edits.updateAt(0, "name"); !ok || v != "z" {
		t.Errorf("undo did not restore the discarded queue: %q ok=%v", v, ok)
	}
}

// TestClearGridUndoEmptiesBothStacks covers what setEditContext relies on when a
// new result replaces the queue.
func TestClearGridUndoEmptiesBothStacks(t *testing.T) {
	c := newUndoTestClient()
	c.setCellValue(0, 1, "z")
	c.undoGridEdit()
	if len(c.grid.undoStack) == 0 && len(c.grid.redoStack) == 0 {
		t.Fatal("precondition: expected non-empty stacks")
	}
	c.clearGridUndo()
	if len(c.grid.undoStack) != 0 || len(c.grid.redoStack) != 0 {
		t.Errorf("clearGridUndo left undo=%d redo=%d", len(c.grid.undoStack), len(c.grid.redoStack))
	}
}

// TestUndoStackIsCapped keeps a long editing session from growing history
// without bound.
func TestUndoStackIsCapped(t *testing.T) {
	c := newUndoTestClient()
	for i := 0; i < gridUndoLimit+20; i++ {
		c.setCellValue(0, 1, "v")
	}
	if len(c.grid.undoStack) != gridUndoLimit {
		t.Errorf("undo stack = %d, want capped at %d", len(c.grid.undoStack), gridUndoLimit)
	}
}
