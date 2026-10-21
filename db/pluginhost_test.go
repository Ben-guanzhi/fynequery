package db

// End-to-end test of the plugin host against the fakeplugin executable
// (db/testdata/fakeplugin): spawn, handshake, open/tables/columns/query
// round trips, plus failure paths. No real database involved.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fynequery/proto"
)

// buildFakePlugin compiles the testdata plugin into dir and returns its path.
func buildFakePlugin(t *testing.T) string {
	t.Helper()
	// Tests run with cwd = db/, so the testdata package path is direct.
	exe := filepath.Join(t.TempDir(), "fq-plugin-fake.exe")
	cmd := exec.Command("go", "build", "-o", exe, "./testdata/fakeplugin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fakeplugin: %v\n%s", err, out)
	}
	return exe
}

func TestPluginEndToEnd(t *testing.T) {
	exe := buildFakePlugin(t)

	p, err := spawnPlugin(exe)
	if err != nil {
		t.Fatalf("spawnPlugin: %v", err)
	}
	defer p.kill()

	if got := p.Info(); got.Code != "fake" || got.Name != "FakeDB" || !got.Plugin {
		t.Fatalf("Info = %+v", got)
	}

	sess, err := p.Connect(proto.ConnectParams{Name: "t1", Host: "localhost", Database: "db"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	if got := sess.Server(); got != "FakeDB 1.2.3" {
		t.Fatalf("Server = %q", got)
	}

	objs, err := sess.Tables()
	if err != nil || len(objs) != 2 || objs[0].Name != "alpha" || objs[0].Rows != 42 {
		t.Fatalf("Tables = %+v, err %v", objs, err)
	}

	cols, err := sess.Columns("alpha")
	if err != nil || len(cols) != 2 || cols[0].Key != "PK" {
		t.Fatalf("Columns = %+v, err %v", cols, err)
	}

	res, err := sess.Query("SELECT 1", 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.DurationMS < 0 {
		t.Fatalf("unexpected negative duration %d", res.DurationMS)
	}

	// Browse reaches the dialect BrowseSQL path through the same pipeline.
	if _, err := sess.Browse("alpha", 5); err != nil {
		t.Fatalf("Browse: %v", err)
	}
}

func TestPluginConnectFailure(t *testing.T) {
	exe := buildFakePlugin(t)

	p, err := spawnPlugin(exe)
	if err != nil {
		t.Fatalf("spawnPlugin: %v", err)
	}
	defer p.kill()

	if _, err := p.Connect(proto.ConnectParams{Name: "t", Host: "boom"}); err == nil {
		t.Fatal("expected connect error for host 'boom', got nil")
	}
	// The plugin must still be usable after a failed open.
	sess, err := p.Connect(proto.ConnectParams{Name: "t", Host: "ok"})
	if err != nil {
		t.Fatalf("Connect after failure: %v", err)
	}
	sess.Close()
}

func TestPluginUnknownMethod(t *testing.T) {
	exe := buildFakePlugin(t)

	p, err := spawnPlugin(exe)
	if err != nil {
		t.Fatalf("spawnPlugin: %v", err)
	}
	defer p.kill()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = p.call(ctx, "nope", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("expected unknown-method error, got %v", err)
	}
}

func TestDiscoverySkipsMissingDirs(t *testing.T) {
	DiscoverPlugins(filepath.Join(os.TempDir(), "definitely-not-here-42"))
	// The built-in mysql driver must remain registered regardless.
	if _, ok := Driver("mysql"); !ok {
		t.Fatal("mysql driver missing after discovery")
	}
}
