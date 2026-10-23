package db

import (
	"sync"
	"testing"

	"fynequery/proto"
)

// poolFakeSession implements Session and records Close calls. It also
// optionally implements Updater to exercise capability forwarding through the
// pool wrapper.
type poolFakeSession struct {
	mu      sync.Mutex
	closedN int
	id      string
	updater bool
}

func (f *poolFakeSession) Server() string                      { return f.id }
func (f *poolFakeSession) Tables() ([]proto.ObjectInfo, error) { return nil, nil }
func (f *poolFakeSession) Columns(string) ([]proto.ColumnInfo, error) {
	return nil, nil
}
func (f *poolFakeSession) Query(string, int) (proto.QueryResult, error) {
	return proto.QueryResult{}, nil
}
func (f *poolFakeSession) Browse(string, int) (proto.QueryResult, error) {
	return proto.QueryResult{}, nil
}
func (f *poolFakeSession) Close() {
	f.mu.Lock()
	f.closedN++
	f.mu.Unlock()
}
func (f *poolFakeSession) UpdateRow(string, map[string]*string, map[string]*string) error {
	return nil
}
func (f *poolFakeSession) DeleteRow(string, map[string]*string) error { return nil }
func (f *poolFakeSession) InsertRow(string, map[string]*string) error { return nil }
func (f *poolFakeSession) ApplyChanges(string, []RowEdit, []map[string]*string, []map[string]*string, []string) error {
	return nil
}

func (f *poolFakeSession) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closedN
}

func newPoolFake(id string, updater bool) *poolFakeSession {
	return &poolFakeSession{id: id, updater: updater}
}

func TestSessionPoolReuseAndEvict(t *testing.T) {
	p := NewSessionPool(2, defaultPoolIdleTTL)
	var opens int
	open := func() (Session, error) {
		opens++
		return newPoolFake("s", true), nil
	}

	s1, err := p.Get("fp1", open)
	if err != nil {
		t.Fatal(err)
	}
	if opens != 1 {
		t.Fatalf("expected 1 open, got %d", opens)
	}
	// Capability forwarding must work through the pool wrapper.
	if _, ok := AsUpdater(s1); !ok {
		t.Fatal("pooled session should forward Updater")
	}

	// Close releases (does not disconnect); the inner session is not closed.
	s1.Close()
	if f := s1.(*poolSession).ent.sess.(*poolFakeSession); f.closeCount() != 0 {
		t.Fatalf("inner should not be closed on release, got %d", f.closeCount())
	}
	if p.Len() != 1 {
		t.Fatalf("pool should hold 1 entry after release, got %d", p.Len())
	}

	// Second Get with the same fingerprint reuses the idle session (no new
	// open) and the same inner instance.
	s2, err := p.Get("fp1", open)
	if err != nil {
		t.Fatal(err)
	}
	if opens != 1 {
		t.Fatalf("expected reuse (still 1 open), got %d", opens)
	}
	inner2 := s2.(*poolSession).ent.sess.(*poolFakeSession)
	if inner2.closeCount() != 0 {
		t.Fatal("reused inner must not have been closed")
	}
	s2.Close()

	// Exceeding maxIdle evicts the oldest idle entry (its inner Close runs).
	for i := 0; i < 3; i++ {
		s, e := p.Get("extra-"+string(rune('a'+i)), func() (Session, error) {
			return newPoolFake("x", false), nil
		})
		if e != nil {
			t.Fatal(e)
		}
		s.Close()
	}
	if p.Len() > 2 {
		t.Fatalf("maxIdle=2 but pool holds %d", p.Len())
	}
}

func TestSessionPoolCloseAll(t *testing.T) {
	p := NewSessionPool(8, defaultPoolIdleTTL)
	f := newPoolFake("c", false)
	_, err := p.Get("fp", func() (Session, error) { return f, nil })
	if err != nil {
		t.Fatal(err)
	}
	p.CloseAll()
	if f.closeCount() != 1 {
		t.Fatalf("CloseAll should close the cached inner, got %d", f.closeCount())
	}
	if p.Len() != 0 {
		t.Fatalf("pool should be empty after CloseAll, got %d", p.Len())
	}
	// Close after CloseAll is a no-op (idempotent).
	if f.closeCount() != 1 {
		t.Fatalf("double close must not call inner twice, got %d", f.closeCount())
	}
}
