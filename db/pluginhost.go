package db

// Plugin host: discovers plugin executables next to the app and exposes each
// one as a Dialer. The process plumbing — spawn, banner handshake, request
// framing, streaming, shutdown — lives in the kernel's connection-oriented
// host (dbcore/plugindriver): ONE plugin process serves many connections, so
// every tab in the UI shares it. What stays here is the dialer metadata, the
// fynequery session surface (transactions, DML batches, metrics, streaming
// queries) through the kernel's raw Call/CallStream, and the Bind view for
// kernel-shaped consumers.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fynequery/proto"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

const pluginPrefix = "fq-plugin-"

// DiscoverPlugins scans dir (and ./plugins under the working directory as a
// development fallback) for plugin executables and registers each one —
// lazily: nothing is spawned here. The driver code comes from the executable
// name ("fq-plugin-<code>[.exe]", the convention isPluginFile enforces), so
// the driver picker can list every engine at startup without paying a process
// per engine; the first Connect (or Bind) starts the process and replaces the
// provisional display info with what the plugin's meta call reports. Missing
// directories are skipped silently — the built-in drivers keep working
// regardless.
func DiscoverPlugins(dir string) {
	seen := map[string]bool{}
	for _, d := range pluginSearchDirs(dir) {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isPluginFile(e.Name()) {
				continue
			}
			code := pluginCode(e.Name())
			if code == "" {
				continue
			}
			// The first file naming a code wins, matching the old
			// first-registered-wins dedup by announced code.
			if seen[code] {
				logf("pluginhost: duplicate plugin for %s skipped: %s", code, e.Name())
				continue
			}
			seen[code] = true
			proc := &pluginProc{
				info: lazyDriverInfo(code),
				exe:  filepath.Join(d, e.Name()),
			}
			procsMu.Lock()
			procs = append(procs, proc)
			procsMu.Unlock()
			Register(proc)
			logf("pluginhost: registered %s (lazy) from %s", code, e.Name())
		}
	}
}

// pluginCode derives the driver code from an executable name:
// "fq-plugin-<code>[.exe]".
func pluginCode(name string) string {
	code := strings.TrimPrefix(name, pluginPrefix)
	if runtime.GOOS == "windows" {
		code = strings.TrimSuffix(code, ".exe")
	}
	return code
}

// pluginDisplayNames covers the engines shipped with the app; a plugin whose
// code is not listed shows its code until the first start corrects it from
// its meta call.
var pluginDisplayNames = map[string]string{
	"postgres": "PostgreSQL",
	"mssql":    "SQL Server",
	"oracle":   "Oracle",
	"sqlite":   "SQLite",
}

// lazyDriverInfo builds the metadata a not-yet-started plugin answers with:
// the code from the file name, a display name from the table above, and the
// kernel's conventional port for the engine. SQLite has no port.
func lazyDriverInfo(code string) DriverInfo {
	info := DriverInfo{Code: code, Name: code, Plugin: true}
	if name, ok := pluginDisplayNames[code]; ok {
		info.Name = name
	}
	if port := dbcore.DefaultPort(dbcore.DatabaseKind(code)); port != 0 {
		info.DefaultPort = strconv.Itoa(port)
	}
	return info
}

func pluginSearchDirs(primary string) []string {
	dirs := []string{}
	if primary != "" {
		dirs = append(dirs, primary)
	}
	if wd, err := os.Getwd(); err == nil {
		dev := filepath.Join(wd, "plugins")
		if dev != primary {
			dirs = append(dirs, dev)
		}
	}
	return dirs
}

func isPluginFile(name string) bool {
	if !strings.HasPrefix(name, pluginPrefix) || strings.HasPrefix(name, ".") {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.HasSuffix(strings.ToLower(name), ".exe")
	}
	return !strings.Contains(name, ".")
}

// Shutdown kills every spawned plugin process and flushes the session pool.
// Called on app exit.
func Shutdown() {
	DefaultPool.CloseAll()
	procsMu.Lock()
	list := append([]*pluginProc(nil), procs...)
	procs = nil
	procsMu.Unlock()
	for _, p := range list {
		p.kill()
	}
}

// logf is swappable so the UI can route plugin diagnostics to its log file.
var logf = func(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// SetLogger installs a new diagnostics sink for the plugin host.
func SetLogger(f func(format string, args ...interface{})) { logf = f }

var (
	procsMu sync.Mutex
	procs   []*pluginProc
)

// pluginProc is one plugin executable acting as a Dialer. Discovery registers
// it from the executable's name alone; the process underneath (the
// connection-oriented kernel host — one process, many connections, the tabs
// all share it) starts on first use, so an engine the session never opens
// never costs a process. The session surface (this file's remoteSession) goes
// through ensure.
type pluginProc struct {
	info DriverInfo
	exe  string

	mu   sync.Mutex
	conn *plugindriver.ConnDriver
}

// startPlugin launches the plugin process, completes the banner handshake and
// fetches the plugin's meta call. It is the lazy path's one expensive step.
func startPlugin(exe string) (*plugindriver.ConnDriver, proto.MetaResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := plugindriver.StartConnWithOpts(ctx, exe, nil, plugindriver.StartOpts{
		// Plugin diagnostics go to the host log, prefixed per line.
		Stderr:        newPrefixWriter(filepath.Base(exe)),
		HideWindow:    true,
		BannerTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, proto.MetaResult{}, fmt.Errorf("%s: %w", exe, err)
	}

	// The kernel handshake verified the banner; the dialer metadata (display
	// name, default port) still comes from the plugin's meta call.
	metaCtx, metaCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer metaCancel()
	var meta proto.MetaResult
	if err := conn.Call(metaCtx, proto.MethodMeta, nil, &meta); err != nil {
		_ = conn.Close()
		return nil, proto.MetaResult{}, fmt.Errorf("%s: meta: %w", exe, err)
	}
	if meta.Code == "" {
		_ = conn.Close()
		return nil, proto.MetaResult{}, fmt.Errorf("%s: plugin reported empty driver code", exe)
	}
	return conn, meta, nil
}

// spawnPlugin starts a plugin immediately (the lazy path's helper, and what
// the tests use): the process runs from now, with the meta call's authoritative
// info.
func spawnPlugin(exe string) (*pluginProc, error) {
	conn, meta, err := startPlugin(exe)
	if err != nil {
		return nil, err
	}
	return &pluginProc{
		info: DriverInfo{
			Code:        meta.Code,
			Name:        meta.Name,
			Version:     meta.Version,
			DefaultPort: meta.DefaultPort,
			Plugin:      true,
		},
		exe:  exe,
		conn: conn,
	}, nil
}

// ensure returns a live plugin process, starting (or restarting) it on demand.
// The first start also corrects the provisional display info with the plugin's
// own meta call, so the driver picker shows the real name and port once any
// connection has been made.
//
// A process that died — crash, or an earlier kill — is reaped and started
// fresh: whatever asked for service gets a working process, so a reconnect
// after a lock-screen DisconnectAll also works without an explicit restart.
// Sessions opened on the dead process recover the same way (remoteSession's
// live), because their database handles died with the process and are opened
// again on the new one.
func (p *pluginProc) ensure() (*plugindriver.ConnDriver, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		if !p.conn.Dead() {
			return p.conn, nil
		}
		logf("pluginhost: %s 进程已退出，重新拉起", filepath.Base(p.exe))
		_ = p.conn.Close()
		p.conn = nil
	}
	conn, meta, err := startPlugin(p.exe)
	if err != nil {
		return nil, err
	}
	if meta.Code != "" && meta.Code != p.info.Code {
		logf("pluginhost: %s announces code %q, registered as %q", filepath.Base(p.exe), meta.Code, p.info.Code)
	}
	if meta.Name != "" {
		p.info.Name = meta.Name
	}
	if meta.Version != "" {
		p.info.Version = meta.Version
	}
	if meta.DefaultPort != "" {
		p.info.DefaultPort = meta.DefaultPort
	}
	p.conn = conn
	return conn, nil
}

// newPrefixWriter renders the plugin's stderr lines in the host log with the
// executable's name in front, the way the built-in host always did.
func newPrefixWriter(name string) *prefixWriter {
	return &prefixWriter{prefix: "[" + name + "] "}
}

type prefixWriter struct {
	mu     sync.Mutex
	prefix string
	buf    []byte
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if strings.TrimSpace(line) != "" {
			logf("%s%s", w.prefix, line)
		}
	}
	return len(p), nil
}

// kill shuts the plugin process down; a never-started process has nothing to
// kill. Not idempotent-guarded: Close is.
func (p *pluginProc) kill() {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// Info reports the dialer metadata. Before the first start this is the
// provisional info derived from the executable's name (see lazyDriverInfo);
// the first start corrects it from the plugin's meta call. Never starts the
// process: enumerating the driver picker must stay free.
func (p *pluginProc) Info() DriverInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

// call performs one round trip in the plugin's own vocabulary — the methods
// the kernel has no view of included — through the kernel host.
func (p *pluginProc) call(ctx context.Context, method string, params, result interface{}) error {
	conn, err := p.ensure()
	if err != nil {
		return err
	}
	return conn.Call(ctx, method, params, result)
}

// callStream performs a streaming exchange through the kernel host, handing
// each frame to the caller in fynequery's shape.
func (p *pluginProc) callStream(ctx context.Context, method string, params interface{}, onFrame func(proto.StreamFrame) (bool, error), final interface{}) error {
	conn, err := p.ensure()
	if err != nil {
		return err
	}
	return conn.CallStream(ctx, method, params, func(f plugindriver.StreamFrame) (bool, error) {
		return onFrame(streamFrameOf(f))
	}, final)
}

// streamFrameOf maps the kernel's frame onto the fynequery shape: the columns
// carry the "PK" key marker the grid expects instead of the kernel's boolean.
func streamFrameOf(f plugindriver.StreamFrame) proto.StreamFrame {
	out := proto.StreamFrame{
		Rows:         f.Rows,
		RowsAffected: f.RowsAffected,
		DurationMS:   f.DurationMS,
	}
	for _, col := range f.Columns {
		c := proto.ColumnInfo{Name: col.Name, Type: col.Type, Nullable: col.Nullable}
		if col.IsPrimaryKey {
			c.Key = "PK"
		}
		out.Columns = append(out.Columns, c)
	}
	return out
}

func (p *pluginProc) Connect(cfg proto.ConnectParams) (Session, error) {
	conn, err := p.ensure()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Info().Code, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connID, server, err := conn.Open(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Info().Name, err)
	}
	// Wide, not Bind: the session drives the connection's transaction, edits,
	// metrics and streaming, and those calls are now typed in the kernel rather
	// than spelled here in the plugin's private vocabulary.
	return &remoteSession{p: p, connID: connID, server: server, wide: conn.Wide(connID, cfg.Database), cfg: cfg}, nil
}

// Bind hands out the kernel's SQLDriver view of one connection of this plugin
// process — the connection-oriented shape: many tabs share the process, each
// bound view addresses exactly one connection.
func (p *pluginProc) Bind(connID, database string) dbcore.SQLDriver {
	conn, err := p.ensure()
	if err != nil {
		logf("pluginhost: bind %s: %v", p.Info().Code, err)
		return nil
	}
	return conn.Bind(connID, database)
}

// ConnIDs lists the connections this plugin process currently holds; a plugin
// that has never started holds none (and starting it for an inventory question
// would defeat the lazy loading).
func (p *pluginProc) ConnIDs() []string {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.ConnIDs()
}

// remoteSession is one connection inside a plugin process.
//
// The session surface below (transactions, row edits, metrics, streaming) is the
// kernel's wide session — see dbcore's SessionTx / RowWriter / BatchWriter /
// MetricsSource / StreamQuerier. This type only adapts it: the UI speaks
// nullable strings, the kernel speaks dbcore.Value, and the conversion happens
// here at the boundary. The transaction mirror the UI reads lives in the kernel
// too (it is a property of the connection, not of this view of it).
type remoteSession struct {
	p      *pluginProc
	connID string
	server string
	wide   *plugindriver.Conn
	// cfg is what opened the database connection. The process underneath can
	// die and be restarted lazily (pluginProc.ensure); live() then re-opens
	// the connection from these params on the new process.
	cfg proto.ConnectParams

	// cancelAsked records that KillQuery was called for the statement currently
	// running, the same flag the built-in MySQL driver keeps: when the query
	// then fails, it reports the clean db.ErrQueryCancelled instead of the
	// driver's raw "context canceled" noise. Reset at every query start.
	cancelAsked atomic.Bool
}

func (s *remoteSession) Server() string { return s.server }

// live makes sure the session's connection is usable: while the plugin process
// is alive it is a cheap flag read; after a restart (or before the first use,
// for sessions inherited from a dead process) it re-opens the database
// connection on the current process and refreshes the connection id and wide
// view. Every process-touching session method goes through it first, so a
// crashed plugin heals on the next call instead of erroring forever.
func (s *remoteSession) live(ctx context.Context) error {
	if s.wide != nil && !s.wide.Dead() {
		return nil
	}
	conn, err := s.p.ensure()
	if err != nil {
		return err
	}
	connID, server, err := conn.Open(ctx, s.cfg)
	if err != nil {
		return fmt.Errorf("%s: %w", s.p.Info().Name, err)
	}
	s.connID, s.server = connID, server
	s.wide = conn.Wide(connID, s.cfg.Database)
	return nil
}

func (s *remoteSession) Tables() ([]proto.ObjectInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return nil, err
	}
	var res proto.TablesResult
	err := s.p.call(ctx, proto.MethodTables, proto.TablesParams{ConnID: s.connID}, &res)
	return res.Objects, err
}

func (s *remoteSession) Columns(table string) ([]proto.ColumnInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return nil, err
	}
	var res proto.ColumnsResult
	err := s.p.call(ctx, proto.MethodColumns, proto.ColumnsParams{ConnID: s.connID, Table: table}, &res)
	return res.Columns, err
}

func (s *remoteSession) Query(sqlText string, maxRows int) (proto.QueryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginQueryTimeout)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return proto.QueryResult{}, err
	}
	s.cancelAsked.Store(false)
	var res proto.QueryResult
	err := s.p.call(ctx, proto.MethodQuery, proto.QueryParams{ConnID: s.connID, SQL: sqlText, MaxRows: maxRows}, &res)
	if err != nil && s.cancelAsked.Load() {
		// The user cancelled this statement: surface the clean sentinel.
		return proto.QueryResult{}, ErrQueryCancelled
	}
	return res, err
}

func (s *remoteSession) Browse(table string, limit int) (proto.QueryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginQueryTimeout)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return proto.QueryResult{}, err
	}
	var res proto.QueryResult
	err := s.p.call(ctx, proto.MethodBrowse, proto.BrowseParams{ConnID: s.connID, Table: table, Limit: limit}, &res)
	return res, err
}

// KillQuery terminates the statement currently running on this session: the
// cancel call goes out while the query's own request is still in flight (the
// plugin serves each request in its own goroutine) and the plugin cancels that
// statement's context, which aborts the database/sql call. A plugin built
// before the cancel method answers "unknown method"; the statement then runs
// to completion and the error says so. Implements db.Canceller.
func (s *remoteSession) KillQuery() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.cancelAsked.Store(true)
	if err := s.live(ctx); err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	if err := s.p.call(ctx, proto.MethodCancel, proto.CancelParams{ConnID: s.connID}, nil); err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	return nil
}

func (s *remoteSession) Close() {
	// A session on a dead process has nothing to close: the connection died
	// with the process, and live() would only open a new one to close it.
	if s.wide != nil && s.wide.Dead() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Roll back any transaction left open so the connection goes back to a
	// clean state before it is dropped or returned to the pool. The mirror the
	// kernel keeps is what says whether one is open.
	if s.wide.InTransaction() {
		_ = s.wide.RollbackTx(ctx)
	}
	// Best effort: a failed close just leaves the conn to die with the process.
	_ = s.p.call(ctx, proto.MethodClose, proto.CloseParams{ConnID: s.connID}, nil)
}

// ---- manual transactions (implements db.Transactional) ---------------------
//
// The plugin is the source of truth for BEGIN/COMMIT/ROLLBACK; the kernel's
// wide session drives it and mirrors the state, so the UI can show it and a
// transaction left open on disconnect can be rolled back.

func (s *remoteSession) Begin() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return err
	}
	return s.wide.BeginTx(ctx)
}

func (s *remoteSession) Commit() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return err
	}
	return s.wide.CommitTx(ctx)
}

func (s *remoteSession) Rollback() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return err
	}
	return s.wide.RollbackTx(ctx)
}

// InTransaction reports the mirrored transaction state the kernel keeps for
// this connection. A dead process holds no transaction — the restart dropped
// it with the connection.
func (s *remoteSession) InTransaction() bool {
	return s.wide != nil && !s.wide.Dead() && s.wide.InTransaction()
}

// Metrics forwards a server-metrics request to the plugin, reusing the already
// opened connection.
func (s *remoteSession) Metrics() ([]proto.Metric, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return nil, err
	}
	metrics, err := s.wide.ServerMetrics(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]proto.Metric, 0, len(metrics))
	for _, m := range metrics {
		out = append(out, proto.Metric{Label: m.Label, Value: m.Value, Group: m.Group})
	}
	return out, nil
}

// QueryStream streams a statement's result from the plugin in batches over the
// multi-frame protocol, emitting each batch to the caller. Statements run
// inside an active manual transaction when one is open (the plugin routes them
// through the transaction).
func (s *remoteSession) QueryStream(sqlText string, batch int, emit func(cols []proto.ColumnInfo, rows [][]*string) (bool, error)) (proto.QueryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginQueryTimeout)
	defer cancel()
	s.cancelAsked.Store(false)
	if err := s.live(ctx); err != nil {
		return proto.QueryResult{}, err
	}
	result, err := s.wideQueryStream(ctx, sqlText, batch, emit)
	if err != nil {
		if s.cancelAsked.Load() {
			return proto.QueryResult{}, ErrQueryCancelled
		}
		return proto.QueryResult{}, err
	}
	return proto.QueryResult{RowsAffected: result.AffectedRows, DurationMS: result.DurationMS}, nil
}

// wideQueryStream is QueryStream's body after the recovery point: the kernel's
// wide session owns the streaming protocol; this is only the shape adaptation
// back to the UI (see stringsOf / columnsOf). DurationMS comes from the
// session's own terminal frame when it reports one; otherwise it stays 0 and
// the caller falls back to the elapsed time it measured itself (query.go).
func (s *remoteSession) wideQueryStream(ctx context.Context, sqlText string, batch int, emit func(cols []proto.ColumnInfo, rows [][]*string) (bool, error)) (*dbcore.StreamResult, error) {
	return s.wide.QueryStream(ctx, sqlText, batch, func(b dbcore.StreamBatch) (bool, error) {
		return emit(columnsOf(b.Columns), stringsOf(b.Rows))
	})
}

// valuesOf renders the UI's nullable strings as the kernel's values. A nil
// pointer is a NULL; an empty string is an empty string.
func valuesOf(cells map[string]*string) map[string]dbcore.Value {
	out := make(map[string]dbcore.Value, len(cells))
	for name, value := range cells {
		if value == nil {
			out[name] = dbcore.Value{Kind: dbcore.ValNull}
			continue
		}
		out[name] = dbcore.Value{Kind: dbcore.ValText, S: *value}
	}
	return out
}

// columnsOf renders the kernel's column metadata in the UI's shape, carrying
// the primary-key marker across as "PK".
func columnsOf(cols []dbcore.ColumnInfo) []proto.ColumnInfo {
	out := make([]proto.ColumnInfo, 0, len(cols))
	for _, c := range cols {
		col := proto.ColumnInfo{Name: c.Name, Type: c.Type, Nullable: c.Nullable}
		if c.IsPrimaryKey {
			col.Key = "PK"
		}
		out = append(out, col)
	}
	return out
}

// stringsOf renders the kernel's values as the UI's nullable strings: a NULL is
// a nil pointer, which is what the grid shows and edits.
func stringsOf(rows [][]dbcore.Value) [][]*string {
	out := make([][]*string, len(rows))
	for i, row := range rows {
		cells := make([]*string, len(row))
		for j, v := range row {
			if v.Kind == dbcore.ValNull {
				continue
			}
			text := v.String()
			cells[j] = &text
		}
		out[i] = cells
	}
	return out
}

// taggedRowsToStrings renders the wire's tagged rows as the UI's nullable
// strings. The built-in drivers (mysql.go, redis.go) still hand the UI rows in
// this form; the plugin path goes through the kernel's values now.
func taggedRowsToStrings(rows [][]proto.CellPayload) [][]*string {
	out := make([][]*string, len(rows))
	for i, row := range rows {
		cells := make([]*string, len(row))
		for j, cell := range row {
			if cell.Kind == "null" || cell.Kind == "" {
				continue
			}
			text := proto.CellText(cell)
			cells[j] = &text
		}
		out[i] = cells
	}
	return out
}

// taggedRowsOf converts the UI's rows into the tagged cells the wire carries.
func taggedRowsOf(rows [][]*string) [][]proto.CellPayload {
	out := make([][]proto.CellPayload, len(rows))
	for i, row := range rows {
		cells := make([]proto.CellPayload, len(row))
		for j, value := range row {
			if value == nil {
				cells[j] = proto.CellPayload{Kind: "null"}
				continue
			}
			cells[j] = proto.CellPayload{Kind: "text", Text: *value}
		}
		out[i] = cells
	}
	return out
}

// ---- row-level DML (implements db.Updater / db.BatchUpdater) ----------------
//
// Plugin connections were historically read-only because the protocol had no
// DML methods. They now forward edits to the plugin process (which generates
// dialect-correct SQL centrally), so every engine the app can browse it can
// also edit — closing the gap with the built-in MySQL driver.

// UpdateRow/InsertRow/DeleteRow forward single-row edits to the plugin. The
// batch path (ApplyChanges) is preferred by the UI, but the one-at-a-time
// fallback also works for sessions without transaction support.
func (s *remoteSession) UpdateRow(table string, pk, changes map[string]*string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return err
	}
	return s.wide.UpdateRow(ctx, table, valuesOf(pk), valuesOf(changes))
}

func (s *remoteSession) InsertRow(table string, values map[string]*string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return err
	}
	return s.wide.InsertRow(ctx, table, valuesOf(values))
}

func (s *remoteSession) DeleteRow(table string, pk map[string]*string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return err
	}
	return s.wide.DeleteRow(ctx, table, valuesOf(pk))
}

// ApplyChanges forwards the whole commit to the plugin, which runs it inside
// one transaction with the same affect-exactly-one-row guarantee as the
// built-in driver.
func (s *remoteSession) ApplyChanges(table string, updates []RowEdit, deletes []map[string]*string, inserts []map[string]*string, ddl []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return err
	}
	req := dbcore.BatchRequest{Table: table, DDL: ddl}
	for _, u := range updates {
		req.Updates = append(req.Updates, dbcore.RowChange{PK: valuesOf(u.PK), Changes: valuesOf(u.Changes)})
	}
	for _, d := range deletes {
		req.Deletes = append(req.Deletes, valuesOf(d))
	}
	for _, i := range inserts {
		req.Inserts = append(req.Inserts, valuesOf(i))
	}
	return s.wide.ApplyBatch(ctx, req)
}

// CreateDatabase forwards the request to the plugin process, reusing the already
// opened connection (postgres/mssql create databases from any open connection).
func (s *remoteSession) CreateDatabase(name string, opts map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.live(ctx); err != nil {
		return err
	}
	return s.wide.CreateDatabase(ctx, name, opts)
}

// pluginQueryTimeout matches the statement cap inside plugins; the host side
// must not give up before the plugin reports its own timeout.
const pluginQueryTimeout = 11 * time.Minute
