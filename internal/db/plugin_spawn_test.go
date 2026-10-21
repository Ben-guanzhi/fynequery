package db

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPluginSubprocessConnects exercises the real subprocess path - spawn the
// built postgresql driver plugin, open a session, and ping a closed port - which
// the in-process pipe test in drivercore cannot cover (exec.Command + real stdio
// framing). It skips unless the plugin binary has been built (task build-plugins),
// so a plain `go test` stays green without it.
func TestPluginSubprocessConnects(t *testing.T) {
	name := "postgresql"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join("..", "..", "plugins", name)
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("driver plugin not built (run task build-plugins): %v", err)
	}
	// Point the state dir at the repo root so PluginsDir resolves to the
	// plugins/ folder the binary was built into - no copy needed.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	t.Setenv("TABLEPRO_STATE_DIR", root)

	cfg := ConnConfig{
		ID: "x", Name: "x", Type: DbTypePostgres,
		Host: "127.0.0.1", Port: 1, Username: "u", Password: "p", Database: "d",
	}
	eng, err := openEngine(cfg)
	if err != nil {
		t.Fatalf("openEngine (spawn + open session): %v", err)
	}
	defer eng.Close()

	// sql.Open is lazy, so the open succeeds; the ping is what actually dials,
	// and a closed port must come back as a driver connection error carried over
	// the protocol - not a framing/process error.
	if perr := eng.Ping(context.Background()); perr == nil {
		t.Fatal("expected ping to fail against a closed port")
	} else if strings.Contains(perr.Error(), "not running") ||
		strings.Contains(perr.Error(), "decode") ||
		strings.Contains(perr.Error(), "start driver plugin") {
		t.Fatalf("the plugin protocol broke instead of reaching the driver: %v", perr)
	}
}

// TestPluginNotInstalledFailsCleanly pins the install seam: an engine whose
// plugin is absent must fail with the install hint, not a spawn error.
func TestPluginNotInstalledFailsCleanly(t *testing.T) {
	t.Setenv("TABLEPRO_STATE_DIR", t.TempDir())
	if _, err := PluginBinary(DbTypePostgres); err == nil {
		t.Fatal("expected a not-installed error with an empty plugins dir")
	} else if !strings.Contains(err.Error(), "未安装") {
		t.Errorf("error = %v, want the install hint", err)
	}
}
