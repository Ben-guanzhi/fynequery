package db

// Handshake test against the real plugin executables produced by build.ps1
// (dist/plugins). Skips when the dist tree is absent, so plain
// `go test ./...` never depends on a build step.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRealPluginsHandshake(t *testing.T) {
	dir := filepath.Join("..", "dist", "plugins")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("dist/plugins not built (run build.ps1 first)")
	}
	DiscoverPlugins(dir)

	want := map[string]bool{"postgres": false, "mssql": false, "oracle": false}
	for _, code := range []string{"postgres", "mssql", "oracle"} {
		d, ok := Driver(code)
		if !ok {
			t.Errorf("driver %q not registered after discovery", code)
			continue
		}
		want[code] = true
		info := d.Info()
		if !info.Plugin {
			t.Errorf("driver %q should be marked Plugin", code)
		}
		if info.DefaultPort == "" {
			t.Errorf("driver %q missing DefaultPort", code)
		}
	}
	for code, ok := range want {
		if !ok {
			t.Errorf("plugin %q failed to register: %v", code, want)
		}
	}
	Shutdown()
}
