package main

import "testing"

// TestGridFind covers the in-result search loop: term -> hits -> step forward
// -> step back (wrapping) -> highlights -> reset. Mirrors TablePlus in-grid
// spotlight.
func TestGridFind(t *testing.T) {
	s := str
	g := newDataTable(&TableData{
		Cols: []ColumnInfo{{Name: "name", Type: "varchar"}, {Name: "city", Type: "varchar"}},
		Rows: [][]*string{
			{s("Alice"), s("Shanghai")},
			{s("Bob"), s("Berlin")},
			{s("Carol"), s("Lima")},
		},
	}, nil)

	var cur, total int
	g.onFindStatus = func(c, tt int) { cur, total = c, tt }

	g.setFindTerm("li") // Alice(0,0), Berlin(1,1), Lima(2,1)
	if len(g.findHits) != 3 {
		t.Fatalf("hits = %d, want 3 (%v)", len(g.findHits), g.findHits)
	}
	want := []searchHitKey{{0, 0}, {1, 1}, {2, 1}}
	for i, h := range want {
		if g.findHits[i] != h {
			t.Fatalf("hits = %v, want %v", g.findHits, want)
		}
	}
	if cur != 1 || total != 3 {
		t.Fatalf("counter = %d/%d, want 1/3", cur, total)
	}
	if !g.isFindHit(1, 1) || g.isFindHit(1, 0) {
		t.Fatal("highlight set wrong")
	}

	// step forward -> 2/3
	g.stepFind(1)
	if g.findCur != 1 || cur != 2 {
		t.Fatalf("after step fwd: cur=%d counter=%d/%d", g.findCur, cur, total)
	}
	// step back -> 1/3
	g.stepFind(-1)
	if g.findCur != 0 || cur != 1 {
		t.Fatalf("after step back: cur=%d counter=%d/%d", g.findCur, cur, total)
	}
	// step back again wraps to the last hit -> 3/3
	g.stepFind(-1)
	if g.findCur != 2 || cur != 3 {
		t.Fatalf("after wrap back: cur=%d counter=%d/%d, want 2/3", g.findCur, cur, total)
	}
	// step forward from the last wraps to the first -> 1/3
	g.stepFind(1)
	if g.findCur != 0 || cur != 1 {
		t.Fatalf("after wrap fwd: cur=%d counter=%d/%d, want 0/1", g.findCur, cur, total)
	}

	// clearing the term clears hits, highlights and the counter
	g.setFindTerm("")
	if len(g.findHits) != 0 || g.isFindHit(1, 1) || cur != 0 || total != 0 {
		t.Fatalf("clear failed: hits=%v cur=%d total=%d", g.findHits, cur, total)
	}
}

// TestGridFindNoHit checks a term that matches nothing leaves the grid usable:
// no spotlight, 0/0 counter, no panic.
func TestGridFindNoHit(t *testing.T) {
	s := str
	g := newDataTable(&TableData{
		Cols: []ColumnInfo{{Name: "name", Type: "varchar"}},
		Rows: [][]*string{{s("Alice")}, {s("Bob")}},
	}, nil)
	var cur, total int
	g.onFindStatus = func(c, tt int) { cur, total = c, tt }

	g.setFindTerm("zzz")
	if len(g.findHits) != 0 {
		t.Fatalf("hits = %v, want none", g.findHits)
	}
	if cur != 0 || total != 0 {
		t.Fatalf("counter = %d/%d, want 0/0", cur, total)
	}
}

// TestGridFindSurvivesStreaming pins that appendRows re-scans an active term
// so hits keep tracking cells as rows land incrementally (TablePlus
// generation-guarded AppendData).
func TestGridFindSurvivesStreaming(t *testing.T) {
	s := str
	g := newDataTable(&TableData{
		Cols: []ColumnInfo{{Name: "name", Type: "varchar"}},
	}, nil)
	g.setFindTerm("li")
	g.appendRows([][]*string{{s("Alice")}, {s("Bob")}})
	if len(g.findHits) != 1 || g.findHits[0] != (searchHitKey{0, 0}) {
		t.Fatalf("hits after batch1 = %v, want [{0 0}]", g.findHits)
	}
	g.appendRows([][]*string{{s("Lima")}})
	if len(g.findHits) != 2 || g.findHits[1] != (searchHitKey{2, 0}) {
		t.Fatalf("hits after batch2 = %v, want [{0 0} {2 0}]", g.findHits)
	}
}
