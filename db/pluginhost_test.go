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
	// The NULL must survive the pipe as a NULL and the empty string as an
	// empty string: on the old all-string wire the two were the same value,
	// which is exactly what the tagged cell form fixes. Asserted end to end
	// through the spawned plugin process, the wire, and the host's conversion.
	if len(res.Rows) != 3 || res.Rows[1][0].Kind != "null" {
		t.Fatalf("NULL did not survive the wire: %+v", res.Rows)
	}
	if res.Rows[2][0].Kind != "text" || res.Rows[2][0].Text != "" {
		t.Fatalf("empty string did not survive the wire: %+v", res.Rows[2])
	}
	ui := res.StringRows()
	if ui[1][0] != nil {
		t.Fatalf("NULL rendered as %q, want nil", *ui[1][0])
	}
	if ui[2][0] == nil || *ui[2][0] != "" {
		t.Fatalf("empty string rendered as %v, want an empty string", ui[2][0])
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

func TestRemoteSessionTransactions(t *testing.T) {
	exe := buildFakePlugin(t)
	p, err := spawnPlugin(exe)
	if err != nil {
		t.Fatalf("spawnPlugin: %v", err)
	}
	defer p.kill()

	sess, err := p.Connect(proto.ConnectParams{Name: "tx", Host: "localhost", Database: "db"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rs, ok := sess.(*remoteSession)
	if !ok {
		t.Fatalf("Connect returned %T, want *remoteSession", sess)
	}

	tx, ok := AsTransactional(sess)
	if !ok {
		t.Fatal("remoteSession should satisfy Transactional via AsTransactional")
	}
	if tx.InTransaction() {
		t.Fatal("session should not start in a transaction")
	}

	if err := tx.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !tx.InTransaction() {
		t.Fatal("InTransaction should be true after Begin")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if tx.InTransaction() {
		t.Fatal("InTransaction should be false after Commit")
	}

	if err := tx.Begin(); err != nil {
		t.Fatalf("Begin (2): %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if tx.InTransaction() {
		t.Fatal("InTransaction should be false after Rollback")
	}

	// Closing with a transaction open must not error (it rolls back internally).
	if err := tx.Begin(); err != nil {
		t.Fatalf("Begin (3): %v", err)
	}
	rs.Close()
	if tx.InTransaction() {
		t.Fatal("Close should roll back and clear the transaction flag")
	}
}

func TestRemoteSessionQueryStream(t *testing.T) {
	exe := buildFakePlugin(t)
	p, err := spawnPlugin(exe)
	if err != nil {
		t.Fatalf("spawnPlugin: %v", err)
	}
	defer p.kill()

	sess, err := p.Connect(proto.ConnectParams{Name: "stream", Host: "localhost", Database: "db"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	st, ok := AsStreamer(sess)
	if !ok {
		t.Fatal("remoteSession should satisfy Streamer via AsStreamer")
	}

	var cols []proto.ColumnInfo
	var got []string
	frames := 0
	final, err := st.QueryStream("SELECT n", 2, func(c []proto.ColumnInfo, rows [][]*string) (bool, error) {
		frames++
		if len(c) > 0 {
			cols = c
		}
		for _, r := range rows {
			got = append(got, *r[0])
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("QueryStream: %v", err)
	}
	if frames != 2 {
		t.Fatalf("got %d frames, want 2", frames)
	}
	if len(cols) != 1 || cols[0].Name != "n" {
		t.Fatalf("columns = %+v", cols)
	}
	if strings.Join(got, ",") != "1,2,3" {
		t.Fatalf("rows = %v, want 1,2,3", got)
	}
	if final.DurationMS != 1 {
		t.Fatalf("terminal duration = %d, want 1", final.DurationMS)
	}

	// Early stop: emit returning keep=false must end the stream without error.
	n := 0
	_, err = st.QueryStream("SELECT n", 2, func(c []proto.ColumnInfo, rows [][]*string) (bool, error) {
		n += len(rows)
		return false, nil // stop after the first frame
	})
	if err != nil {
		t.Fatalf("early-stop QueryStream: %v", err)
	}
	if n != 2 {
		t.Fatalf("early stop consumed %d rows, want 2", n)
	}
}

func TestRemoteSessionMetrics(t *testing.T) {
	exe := buildFakePlugin(t)
	p, err := spawnPlugin(exe)
	if err != nil {
		t.Fatalf("spawnPlugin: %v", err)
	}
	defer p.kill()

	sess, err := p.Connect(proto.ConnectParams{Name: "metrics", Host: "localhost", Database: "db"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	m, ok := AsMetricser(sess)
	if !ok {
		t.Fatal("remoteSession should satisfy Metricser via AsMetricser")
	}
	ms, err := m.Metrics()
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(ms) != 2 || ms[0].Label != "Connections" || ms[0].Value != "7" {
		t.Fatalf("metrics = %+v", ms)
	}
}

// TestDiscoveryLazyLoading pins the lazy-loading contract: discovery registers
// a plugin from its file name alone and starts no process; the first Connect
// spawns it and the provisional display info is corrected from the plugin's
// meta call.
func TestDiscoveryLazyLoading(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "fq-plugin-lazyfake.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "./testdata/fakeplugin").CombinedOutput(); err != nil {
		t.Fatalf("build fakeplugin: %v\n%s", err, out)
	}

	DiscoverPlugins(filepath.Dir(exe))

	d, ok := Driver("lazyfake")
	if !ok {
		t.Fatal("discovery did not register the plugin by file name")
	}
	p, ok := d.(*pluginProc)
	if !ok {
		t.Fatalf("Driver(lazyfake) = %T, want *pluginProc", d)
	}

	// Nothing spawned yet, and the provisional info is usable by the picker.
	p.mu.Lock()
	started := p.conn != nil
	p.mu.Unlock()
	if started {
		t.Fatal("discovery must not spawn the plugin process")
	}
	info := p.Info()
	if !info.Plugin || info.Code != "lazyfake" {
		t.Fatalf("provisional Info = %+v", info)
	}

	// Connect starts the process on demand and works end to end.
	sess, err := d.Connect(proto.ConnectParams{Name: "lazy", Host: "localhost", Database: "db"})
	if err != nil {
		t.Fatalf("Connect (first, spawns): %v", err)
	}
	defer sess.Close()
	defer p.kill() // release the executable so the TempDir cleanup can remove it
	if got := sess.Server(); got != "FakeDB 1.2.3" {
		t.Fatalf("Server = %q", got)
	}

	// The meta call's authoritative info has replaced the provisional one.
	info = p.Info()
	if info.Name != "FakeDB" || info.Version != "test" || info.DefaultPort != "1234" {
		t.Fatalf("Info after first start = %+v, want the meta call's values", info)
	}

	// A second dialer lookup must not re-register over it.
	if again, ok := Driver("lazyfake"); !ok || again != d {
		t.Fatal("registry lost the plugin after start")
	}
}

// TestRemoteSessionKillQueryWirePath pins the cancel protocol round trip: the
// host speaks MethodCancel through the pipe, and the fake plugin — which never
// has a statement running — answers its honest error. The full
// cancel-while-running behaviour is proven against a real statement in
// pluginkit's TestHandlerCancelAbortsRunningQuery.
func TestRemoteSessionKillQueryWirePath(t *testing.T) {
	exe := buildFakePlugin(t)
	p, err := spawnPlugin(exe)
	if err != nil {
		t.Fatalf("spawnPlugin: %v", err)
	}
	defer p.kill()

	sess, err := p.Connect(proto.ConnectParams{Name: "cancel", Host: "localhost", Database: "db"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	c, ok := AsCanceller(sess)
	if !ok {
		t.Fatal("remoteSession should satisfy Canceller via AsCanceller")
	}
	if err := c.KillQuery(); err == nil || !strings.Contains(err.Error(), "no active query") {
		t.Fatalf("KillQuery with nothing running = %v, want the plugin's no-active-query error", err)
	}
}

// TestRemoteSessionRecoversAfterRestart pins the restart semantics: a plugin
// process that died does not poison the session. The next call reaps the dead
// process, starts a fresh one, re-opens the database connection on it, and the
// session goes on — instead of erroring forever until an explicit restart.
func TestRemoteSessionRecoversAfterRestart(t *testing.T) {
	exe := buildFakePlugin(t)
	p, err := spawnPlugin(exe)
	if err != nil {
		t.Fatalf("spawnPlugin: %v", err)
	}
	defer p.kill()

	sess, err := p.Connect(proto.ConnectParams{Name: "rec", Host: "localhost", Database: "db"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	if sess.(*remoteSession).wide.Dead() {
		t.Fatal("a fresh session reports a dead connection")
	}

	// The process dies out from under the session (crash, lock-screen kill —
	// the cause is irrelevant; the kernel flags the corpse either way).
	p.mu.Lock()
	oldConn := p.conn
	p.mu.Unlock()
	if err := oldConn.Close(); err != nil {
		t.Fatalf("simulate death: %v", err)
	}
	if !sess.(*remoteSession).wide.Dead() {
		t.Fatal("session did not notice the process died")
	}
	if sess.(*remoteSession).InTransaction() {
		t.Fatal("a dead process holds no transaction")
	}
	// Closing a dead session is a no-op, not a re-establishment.
	sess.Close()

	// The next call heals: fresh process, fresh connection, same answer.
	tables, err := sess.Tables()
	if err != nil {
		t.Fatalf("Tables after restart: %v", err)
	}
	if len(tables) != 2 {
		t.Fatalf("Tables after restart = %+v, want the fake catalog", tables)
	}
	p.mu.Lock()
	newConn := p.conn
	p.mu.Unlock()
	if newConn == oldConn {
		t.Fatal("ensure did not start a new process")
	}
	if newConn.Dead() {
		t.Fatal("the restarted process reports dead")
	}

	// The wide paths recover through the same live() point: begin/commit on
	// the new connection.
	tx, ok := sess.(Transactional)
	if !ok {
		t.Fatal("remoteSession should satisfy Transactional")
	}
	if err := tx.Begin(); err != nil {
		t.Fatalf("Begin after restart: %v", err)
	}
	if !tx.InTransaction() {
		t.Fatal("Begin after restart did not open a transaction")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit after restart: %v", err)
	}
}
