package db

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestKeyringSealOpenRoundTrip pins the keyring half of the store on every
// platform: a sealed value is a "keyring:" reference that names no password,
// and the shared dispatcher fetches the password back out of the keyring.
func TestKeyringSealOpenRoundTrip(t *testing.T) {
	keyring.MockInit()

	sealed, err := sealViaKeyring("hunter2")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !strings.HasPrefix(sealed, keyringPrefix) {
		t.Fatalf("sealed value is not a keyring reference: %q", sealed)
	}
	if strings.Contains(sealed, "hunter2") {
		t.Fatal("the password leaked into its own keyring reference")
	}
	if !isSealedValue(sealed) {
		t.Fatal("a keyring reference must count as sealed")
	}
	if isSealedValue("plain") || isSealedValue("") {
		t.Fatal("unsealed values must not count as sealed")
	}

	opened, err := unprotectSecret(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened != "hunter2" {
		t.Fatalf("round trip lost the password, got %q", opened)
	}
}

// TestSecretsGC pins the save-time cleanup: entries the previous file pointed
// at that this save dropped are removed from the keyring, entries still
// referenced stay, and a reference to an already-missing entry is not an error.
func TestSecretsGC(t *testing.T) {
	keyring.MockInit()

	keep, err := sealViaKeyring("keep")
	if err != nil {
		t.Fatalf("seal keep: %v", err)
	}
	drop, err := sealViaKeyring("drop")
	if err != nil {
		t.Fatalf("seal drop: %v", err)
	}

	secretsGC([]string{keep, drop, "dpapi:x", "plain"}, []string{keep})

	if _, err := unprotectSecret(keep); err != nil {
		t.Fatalf("an entry still on disk was collected: %v", err)
	}
	if _, err := unprotectSecret(drop); err == nil {
		t.Fatal("the dropped entry should be gone from the keyring")
	} else if !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("want keyring not-found, got %v", err)
	}
	dropID, _ := keyringRefID(drop)
	secretsGC([]string{keyringPrefix + dropID}, nil)
}

// TestProtectFallsBackWhenKeyringFails pins the documented last resort: a
// machine whose keyring refuses everything still saves the connection, with
// the password in clear text and a log line saying so.
func TestProtectFallsBackWhenKeyringFails(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))

	sealed, err := protectViaKeyringOrPlain("pw")
	if err != nil {
		t.Fatalf("fallback must not fail the save: %v", err)
	}
	if sealed != "pw" {
		t.Fatalf("fallback should keep the plain text, got %q", sealed)
	}
}

// TestKeyringWorksProbe pins the capability probe the dialog's warning rests
// on: a working keyring answers true, a broken one false.
func TestKeyringWorksProbe(t *testing.T) {
	keyring.MockInit()
	if !keyringWorks() {
		t.Fatal("a working keyring must probe true")
	}
	keyring.MockInitWithError(errors.New("no secret service"))
	if keyringWorks() {
		t.Fatal("a broken keyring must probe false")
	}
	keyring.MockInit()
}

// TestStoreDeleteCollectsKeyringEntry pins the cleanup path the delete button
// rides on: dropping a connection from the file also drops its keyring entry
// on the next save, while a surviving connection's entry stays openable.
func TestStoreDeleteCollectsKeyringEntry(t *testing.T) {
	keyring.MockInit()
	file := filepath.Join(t.TempDir(), "connections.json")
	s := &Store{live: make(map[string]*Conn), connFile: file}
	s.AddConnection(ConnConfig{ID: "x", Name: "a", Type: DbTypeSQLite, Database: "a.db", Password: "alpha"})
	s.AddConnection(ConnConfig{ID: "y", Name: "b", Type: DbTypeSQLite, Database: "b.db", Password: "bravo"})

	before, ok, err := readJSONFile[ConnConfig](file)
	if err != nil || !ok || len(before) != 2 {
		t.Fatalf("saved file should hold 2 connections, got %d (%v, %v)", len(before), ok, err)
	}
	refOf := map[string]string{}
	for _, cfg := range before {
		refOf[cfg.Database] = cfg.Password
	}

	if err := s.DeleteConnection("c0"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// The deleted connection's keyring entry must be gone; the survivor's must
	// stay. DPAPI values are re-sealed fresh on every save, so only the
	// keyring half of the store has a stable reference to assert on.
	if id, isKeyring := keyringRefID(refOf["a.db"]); isKeyring {
		if _, err := keyring.Get(keyringService, id); err == nil {
			t.Fatal("deleted connection's keyring entry survived the save")
		}
	}
	after, ok, err := readJSONFile[ConnConfig](file)
	if err != nil || !ok || len(after) != 1 {
		t.Fatalf("file should hold 1 connection after delete, got %d (%v, %v)", len(after), ok, err)
	}
	if id, isKeyring := keyringRefID(after[0].Password); isKeyring {
		if _, err := keyring.Get(keyringService, id); err != nil {
			t.Fatalf("surviving connection's keyring entry is gone: %v", err)
		}
	}

	s2 := &Store{live: make(map[string]*Conn), connFile: file}
	s2.loadConnections()
	if len(s2.saved) != 1 || s2.saved[0].Password != "bravo" {
		t.Fatalf("survivor did not round-trip, got %+v", s2.saved)
	}
}
