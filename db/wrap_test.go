package db

import (
	"testing"
)

type pingerSession struct {
	fakePlainSession
	pings int
}

func (s *pingerSession) Ping() error {
	s.pings++
	return nil
}

func TestAsPingerSeesThroughWrapper(t *testing.T) {
	inner := &pingerSession{}
	w := Session(&capabilityWrapper{Session: inner})
	if _, ok := w.(Pinger); !ok {
		t.Fatal("wrapper should forward Pinger")
	}
	p, ok := AsPinger(w)
	if !ok {
		t.Fatal("AsPinger should unwrap to the real session")
	}
	if err := p.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if inner.pings != 1 {
		t.Fatalf("inner Ping called %d times, want 1", inner.pings)
	}
}

func TestAsPingerRejectsPlainSession(t *testing.T) {
	w := Session(&capabilityWrapper{Session: fakePlainSession{}})
	if _, ok := AsPinger(w); ok {
		t.Fatal("AsPinger should report false for a session without native ping")
	}
	if err := w.(Pinger).Ping(); err == nil {
		t.Fatal("wrapper Ping should be unsupported")
	}
}

func TestAsPingerBareSession(t *testing.T) {
	if _, ok := AsPinger(&pingerSession{}); !ok {
		t.Fatal("AsPinger should accept a bare session")
	}
}
