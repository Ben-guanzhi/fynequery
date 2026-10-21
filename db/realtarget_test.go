package db

// Handshake test against the real plugin executables produced by build.ps1
// (dist/plugins). Skips when the dist tree is absent, so plain
// `go test ./...` never depends on a build step.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealPluginsHandshake(t *testing.T) {
	dir := filepath.Join("..", "dist", "plugins")
	// Skip when the build step hasn't run: the directory is either absent or
	// exists without any plugin executables (a plain `go test ./...` must never
	// depend on build.ps1).
	entries, err := os.ReadDir(dir)
	if err != nil || !hasPluginExecutable(entries) {
		t.Skip("dist/plugins has no plugin executables (run build.ps1 first)")
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

// hasPluginExecutable reports whether entries contains at least one built
// plugin executable (fq-plugin-*.exe on Windows).
func hasPluginExecutable(entries []os.DirEntry) bool {
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "fq-plugin-") && strings.HasSuffix(strings.ToLower(name), ".exe") {
			return true
		}
	}
	return false
}
