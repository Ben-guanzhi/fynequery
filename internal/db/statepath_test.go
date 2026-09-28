package db

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveStateFileMigratesOnce covers the move from the beside-the-exe
// layout to the shared config directory: the legacy file is copied the first
// time the new location is still empty, the copy wins over the legacy one from
// then on, and the legacy file is never deleted.
func TestResolveStateFileMigratesOnce(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	exe := filepath.Join(dir, "exe")
	const legacyBody = "beside the executable"
	if err := os.MkdirAll(exe, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", exe, err)
	}
	if err := os.WriteFile(filepath.Join(exe, "keep.txt"), []byte(legacyBody), 0600); err != nil {
		t.Fatalf("seed %s: %v", exe, err)
	}

	want := filepath.Join(config, "keep.txt")
	if got := resolveStateFile(config, exe, "keep.txt"); got != want {
		t.Fatalf("path = %s, want the config copy %s", got, want)
	}
	migrated, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("legacy file was not copied: %v", err)
	}
	if string(migrated) != legacyBody {
		t.Fatalf("migrated contents = %q, want the legacy file verbatim", migrated)
	}
	if _, err := os.Stat(filepath.Join(exe, "keep.txt")); err != nil {
		t.Fatalf("migration deleted the legacy file: %v", err)
	}

	// A file only the new layout has must not drag anything in, and a new file
	// lands in the config directory even when the executable one does not exist
	// (a first install).
	onlyNew := filepath.Join(dir, "fresh")
	if got := resolveStateFile(onlyNew, exe, "history.json"); got != filepath.Join(onlyNew, "history.json") {
		t.Fatalf("path = %s, want it under the config directory", got)
	}
	if _, err := os.Stat(onlyNew); err != nil {
		t.Fatalf("state directory was not created: %v", err)
	}

	// Same directory on both sides (no config dir available): nothing to copy,
	// no copying onto itself.
	if got := resolveStateFile(exe, exe, "keep.txt"); got != filepath.Join(exe, "keep.txt") {
		t.Fatalf("in-place layout = %s, want the same file", got)
	}
}

// TestStateFilePathHonoursEnvOverride pins the test/portability hook: with
// TABLEPRO_STATE_DIR set, every state file - connections, history, ui layout -
// resolves inside it, which is what keeps `go test` off the user's real saved
// connections now that they live in the shared config directory.
func TestStateFilePathHonoursEnvOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv(stateDirEnv, dir)

	for _, name := range []string{"connections.json", "history.json", "ui.json"} {
		got := StateFilePath(name)
		if want := filepath.Join(dir, name); got != want {
			t.Fatalf("StateFilePath(%q) = %s, want %s", name, got, want)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("x"), 0600); err != nil {
		t.Fatalf("the override directory was not created: %v", err)
	}
}

// TestNewStoreUsesTheStateDirectory checks the store is wired to that path
// rather than to whatever directory the test binary happens to sit in.
func TestNewStoreUsesTheStateDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(stateDirEnv, dir)
	if err := os.WriteFile(filepath.Join(dir, "connections.json"),
		[]byte(`[{"id":"c0","name":"shared","type":"sqlite","database":"/tmp/x.db"}]`), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s := NewStore()
	if len(s.saved) != 1 || s.saved[0].Name != "shared" {
		t.Fatalf("saved connections = %+v, want the one from the state directory", s.saved)
	}
}
