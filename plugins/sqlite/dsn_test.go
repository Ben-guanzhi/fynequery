package main

import (
	"strings"
	"testing"
)

// addSQLitePragmas gives a file database the pool-wide pragmas as modernc
// `_pragma` query parameters (WAL journal, a busy timeout, NORMAL sync) —
// the driver runs them on every pooled physical connection. TXN-04's fix
// landed in TableProGo/db-dev first; this is the fynequery plugin's turn.

func TestAddSQLitePragmasFileDSN(t *testing.T) {
	got := addSQLitePragmas("file:app.db?mode=rwc")
	for _, want := range []string{"_pragma=journal_mode", "_pragma=busy_timeout", "_pragma=synchronous"} {
		if !strings.Contains(got, want) {
			t.Errorf("dsn %q missing %s", got, want)
		}
	}
	// The original query parameters must survive.
	if !strings.Contains(got, "mode=rwc") {
		t.Errorf("dsn %q lost mode=rwc", got)
	}
	if strings.Count(got, "?") != 1 {
		t.Errorf("dsn %q must carry exactly one '?', got %d", got, strings.Count(got, "?"))
	}
}

func TestAddSQLitePragmasBarePath(t *testing.T) {
	got := addSQLitePragmas("C:\\data\\app.db")
	if !strings.HasPrefix(got, "C:\\data\\app.db?") {
		t.Errorf("bare path must gain '?', got %q", got)
	}
	if !strings.Contains(got, "busy_timeout") {
		t.Errorf("dsn %q missing busy_timeout", got)
	}
}

func TestAddSQLitePragmasMemoryUntouched(t *testing.T) {
	if got := addSQLitePragmas(":memory:"); got != ":memory:" {
		t.Errorf(":memory: must stay bare, got %q", got)
	}
}

func TestAddSQLitePragmasIdempotent(t *testing.T) {
	once := addSQLitePragmas("file:app.db?mode=rwc")
	twice := addSQLitePragmas(once)
	if twice != once {
		t.Errorf("adding pragmas twice changed the DSN:\n once = %q\ntwice = %q", once, twice)
	}
}
