package main

import (
	"testing"

	"fyne.io/fyne/v2/container"
)

// TestAppendRowsStreaming pins the incremental paint path (TablePlus
// TableContentV4.AppendData): rows land in batches, Fetched grows with each
// batch, and Total tracks the larger of fetched/total. The custom dataTable
// is fully materialised; streaming keeps a large result from freezing the UI
// in one giant rebuild.
func TestAppendRowsStreaming(t *testing.T) {
	s := str
	g := newDataTable(&TableData{
		Cols: []ColumnInfo{{Name: "id", Type: "int"}},
	}, nil)
	if g.data.Fetched != 0 {
		t.Fatalf("fetched = %d, want 0", g.data.Fetched)
	}

	g.appendRows([][]*string{{s("1")}, {s("2")}})
	if g.data.Fetched != 2 || len(g.data.Rows) != 2 {
		t.Fatalf("after batch 1: fetched=%d rows=%d", g.data.Fetched, len(g.data.Rows))
	}
	if g.data.Total != 2 {
		t.Fatalf("total = %d, want 2", g.data.Total)
	}

	g.appendRows([][]*string{{s("3")}})
	if g.data.Fetched != 3 || g.data.Total != 3 {
		t.Fatalf("after batch 2: fetched=%d total=%d", g.data.Fetched, g.data.Total)
	}
	if deref(g.data.Rows[2][0]) != "3" {
		t.Fatalf("row 2 = %q, want 3", deref(g.data.Rows[2][0]))
	}
}

// TestQueryPaneRunGenDiscardsStale pins the generation guard used by run() to
// drop result batches from a superseded query: bumpRun advances strictly and
// the in-run check (myGen != qp.runGen) flips true the moment a newer run
// starts, so stale batches are discarded instead of painting.
func TestQueryPaneRunGenDiscardsStale(t *testing.T) {
	qp := &queryPane{}
	gen1 := qp.bumpRun()
	gen2 := qp.bumpRun()
	if gen1 == gen2 {
		t.Fatalf("generations must be distinct: %d == %d", gen1, gen2)
	}
	if gen1 != 1 || gen2 != 2 {
		t.Fatalf("expected monotonic 1 then 2, got %d/%d", gen1, gen2)
	}

	// Replicates the exact guard inside run()'s streaming loop.
	alive := func(myGen int) bool { return myGen == qp.runGen }
	if alive(gen1) {
		t.Fatal("a superseded generation must be dropped, but reported alive")
	}
	if !alive(gen2) {
		t.Fatal("the current generation must remain alive")
	}

	// Starting a third run supersedes gen2 as well.
	gen3 := qp.bumpRun()
	if alive(gen2) {
		t.Fatal("gen2 should be superseded by gen3")
	}
	if !alive(gen3) {
		t.Fatal("gen3 must be the live generation")
	}
}

// TestQueryPaneResultGridNilUntilRun pins that resultGrid() is nil before a
// query builds its grid, so the in-result find box safely no-ops when there is
// nothing to search.
func TestQueryPaneResultGridNilUntilRun(t *testing.T) {
	qp := &queryPane{}
	if g := qp.resultGrid(); g != nil {
		t.Fatalf("resultGrid should be nil before any run, got %v", g)
	}
	qp.resultSlot = container.NewStack(newDataTable(emptyTableData(), newStatusBar()))
	if g := qp.resultGrid(); g == nil {
		t.Fatal("resultGrid should return the stacked dataTable after a run")
	}
}
