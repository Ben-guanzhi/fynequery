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
	"strings"
	"sync"
	"time"

	"fynequery/proto"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

const pluginPrefix = "fq-plugin-"

// DiscoverPlugins scans dir (and ./plugins under the working directory as a
// development fallback) for plugin executables and registers each one.
// Missing directories or broken plugins are skipped silently — the built-in
// drivers keep working regardless.
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
			proc, err := spawnPlugin(filepath.Join(d, e.Name()))
			if err != nil {
				logf("pluginhost: %v", err)
				continue
			}
			if seen[proc.info.Code] {
				proc.kill()
				continue
			}
			seen[proc.info.Code] = true
			procsMu.Lock()
			procs = append(procs, proc)
			procsMu.Unlock()
			Register(proc)
			logf("pluginhost: registered %s (%s) from %s", proc.info.Name, proc.info.Code, e.Name())
		}
	}
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

// pluginProc is one spawned plugin executable acting as a Dialer: the
// connection-oriented kernel host underneath owns the process (one process,
// many connections — the tabs all share it); this type adds the dialer
// metadata and the session surface.
type pluginProc struct {
	info DriverInfo
	exe  string
	conn *plugindriver.ConnDriver
}

func spawnPlugin(exe string) (*pluginProc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := plugindriver.StartConnWithOpts(ctx, exe, nil, plugindriver.StartOpts{
		// Plugin diagnostics go to the host log, prefixed per line.
		Stderr:        newPrefixWriter(filepath.Base(exe)),
		HideWindow:    true,
		BannerTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", exe, err)
	}

	// The kernel handshake verified the banner; the dialer metadata (display
	// name, default port) still comes from the plugin's meta call.
	metaCtx, metaCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer metaCancel()
	var meta proto.MetaResult
	if err := conn.Call(metaCtx, proto.MethodMeta, nil, &meta); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: meta: %w", exe, err)
	}
	if meta.Code == "" {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: plugin reported empty driver code", exe)
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

func (p *pluginProc) kill() { _ = p.conn.Close() }

func (p *pluginProc) Info() DriverInfo { return p.info }

// call performs one round trip in the plugin's own vocabulary — the methods
// the kernel has no view of included — through the kernel host.
func (p *pluginProc) call(ctx context.Context, method string, params, result interface{}) error {
	return p.conn.Call(ctx, method, params, result)
}

// callStream performs a streaming exchange through the kernel host, handing
// each frame to the caller in fynequery's shape.
func (p *pluginProc) callStream(ctx context.Context, method string, params interface{}, onFrame func(proto.StreamFrame) (bool, error), final interface{}) error {
	return p.conn.CallStream(ctx, method, params, func(f plugindriver.StreamFrame) (bool, error) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connID, server, err := p.conn.Open(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.info.Name, err)
	}
	return &remoteSession{p: p, connID: connID, server: server}, nil
}

// Bind hands out the kernel's SQLDriver view of one connection of this plugin
// process — the connection-oriented shape: many tabs share the process, each
// bound view addresses exactly one connection.
func (p *pluginProc) Bind(connID, database string) dbcore.SQLDriver {
	return p.conn.Bind(connID, database)
}

// ConnIDs lists the connections this plugin process currently holds.
func (p *pluginProc) ConnIDs() []string { return p.conn.ConnIDs() }

// remoteSession is one connection inside a plugin process.
type remoteSession struct {
	p      *pluginProc
	connID string
	server string

	// txMu guards inTx, the client-side view of the manual transaction the
	// plugin holds. The plugin is the source of truth for BEGIN/COMMIT/
	// ROLLBACK; this flag mirrors it so the UI can show state and so a
	// transaction left open on disconnect can be rolled back.
	txMu sync.Mutex
	inTx bool
}

func (s *remoteSession) Server() string { return s.server }

func (s *remoteSession) Tables() ([]proto.ObjectInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var res proto.TablesResult
	err := s.p.call(ctx, proto.MethodTables, proto.TablesParams{ConnID: s.connID}, &res)
	return res.Objects, err
}

func (s *remoteSession) Columns(table string) ([]proto.ColumnInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var res proto.ColumnsResult
	err := s.p.call(ctx, proto.MethodColumns, proto.ColumnsParams{ConnID: s.connID, Table: table}, &res)
	return res.Columns, err
}

func (s *remoteSession) Query(sqlText string, maxRows int) (proto.QueryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginQueryTimeout)
	defer cancel()
	var res proto.QueryResult
	err := s.p.call(ctx, proto.MethodQuery, proto.QueryParams{ConnID: s.connID, SQL: sqlText, MaxRows: maxRows}, &res)
	return res, err
}

func (s *remoteSession) Browse(table string, limit int) (proto.QueryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginQueryTimeout)
	defer cancel()
	var res proto.QueryResult
	err := s.p.call(ctx, proto.MethodBrowse, proto.BrowseParams{ConnID: s.connID, Table: table, Limit: limit}, &res)
	return res, err
}

func (s *remoteSession) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Roll back any transaction left open so the connection goes back to a
	// clean state before it is dropped or returned to the pool.
	s.txMu.Lock()
	inTx := s.inTx
	s.txMu.Unlock()
	if inTx {
		_ = s.p.call(ctx, proto.MethodRollback, proto.TxParams{ConnID: s.connID}, nil)
		s.txMu.Lock()
		s.inTx = false
		s.txMu.Unlock()
	}
	// Best effort: a failed close just leaves the conn to die with the process.
	_ = s.p.call(ctx, proto.MethodClose, proto.CloseParams{ConnID: s.connID}, nil)
}

// ---- manual transactions (implements db.Transactional) ---------------------

// Begin starts a transaction on the plugin connection. The plugin generates the
// BEGIN; this flag mirrors it for the UI and disconnect rollback.
func (s *remoteSession) Begin() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.p.call(ctx, proto.MethodBegin, proto.TxParams{ConnID: s.connID}, nil); err != nil {
		return err
	}
	s.txMu.Lock()
	s.inTx = true
	s.txMu.Unlock()
	return nil
}

// Commit commits the active transaction.
func (s *remoteSession) Commit() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.p.call(ctx, proto.MethodCommit, proto.TxParams{ConnID: s.connID}, nil); err != nil {
		return err
	}
	s.txMu.Lock()
	s.inTx = false
	s.txMu.Unlock()
	return nil
}

// Rollback rolls back the active transaction.
func (s *remoteSession) Rollback() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.p.call(ctx, proto.MethodRollback, proto.TxParams{ConnID: s.connID}, nil); err != nil {
		return err
	}
	s.txMu.Lock()
	s.inTx = false
	s.txMu.Unlock()
	return nil
}

// InTransaction reports the client-side view of the plugin's transaction state.
func (s *remoteSession) InTransaction() bool {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	return s.inTx
}

// Metrics forwards a server-metrics request to the plugin, reusing the already
// opened connection.
func (s *remoteSession) Metrics() ([]proto.Metric, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var res proto.MetricsResult
	if err := s.p.call(ctx, proto.MethodMetrics, proto.MetricsParams{ConnID: s.connID}, &res); err != nil {
		return nil, err
	}
	return res.Metrics, nil
}

// QueryStream streams a statement's result from the plugin in batches over the
// multi-frame protocol, emitting each batch to the caller. Statements run
// inside an active manual transaction when one is open (the plugin routes them
// through the transaction).
func (s *remoteSession) QueryStream(sqlText string, batch int, emit func(cols []proto.ColumnInfo, rows [][]*string) (bool, error)) (proto.QueryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginQueryTimeout)
	defer cancel()
	var final proto.QueryResult
	err := s.p.callStream(ctx, proto.MethodQueryStream,
		proto.QueryStreamParams{ConnID: s.connID, SQL: sqlText, Batch: batch},
		func(f proto.StreamFrame) (bool, error) {
			return emit(f.Columns, taggedRowsToStrings(f.Rows))
		}, &final)
	if err != nil {
		return proto.QueryResult{}, err
	}
	return final, nil
}

// taggedRowsToStrings renders the wire's tagged rows as the UI's nullable strings.
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

// taggedRows converts a list of the UI's row maps into tagged cells.
func taggedRows(rows []map[string]*string) []map[string]proto.CellPayload {
	out := make([]map[string]proto.CellPayload, 0, len(rows))
	for _, row := range rows {
		out = append(out, cellsToTagged(row))
	}
	return out
}

// cellsToTagged renders the UI's nullable strings as the tagged cells the wire
// carries. A nil pointer is a NULL; an empty string is an empty string.
func cellsToTagged(cells map[string]*string) map[string]proto.CellPayload {
	out := make(map[string]proto.CellPayload, len(cells))
	for name, value := range cells {
		if value == nil {
			out[name] = proto.CellPayload{Kind: "null"}
			continue
		}
		out[name] = proto.CellPayload{Kind: "text", Text: *value}
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
	return s.p.call(ctx, proto.MethodUpdate, proto.DMLSingleParams{
		ConnID: s.connID, Table: table, PK: cellsToTagged(pk), Changes: cellsToTagged(changes),
	}, nil)
}

func (s *remoteSession) InsertRow(table string, values map[string]*string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return s.p.call(ctx, proto.MethodInsert, proto.DMLSingleParams{
		ConnID: s.connID, Table: table, Values: cellsToTagged(values),
	}, nil)
}

func (s *remoteSession) DeleteRow(table string, pk map[string]*string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return s.p.call(ctx, proto.MethodDelete, proto.DMLSingleParams{
		ConnID: s.connID, Table: table, PK: cellsToTagged(pk),
	}, nil)
}

// ApplyChanges forwards the whole commit to the plugin, which runs it inside
// one transaction with the same affect-exactly-one-row guarantee as the
// built-in driver.
func (s *remoteSession) ApplyChanges(table string, updates []RowEdit, deletes []map[string]*string, inserts []map[string]*string, ddl []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	up := make([]proto.DMLRow, 0, len(updates))
	for _, u := range updates {
		up = append(up, proto.DMLRow{PK: cellsToTagged(u.PK), Changes: cellsToTagged(u.Changes)})
	}
	err := s.p.call(ctx, proto.MethodApplyBatch, proto.DMLBatchParams{
		ConnID: s.connID, Table: table, Updates: up, Deletes: taggedRows(deletes), Inserts: taggedRows(inserts), DDL: ddl,
	}, nil)
	return err
}

// CreateDatabase forwards the request to the plugin process, reusing the already
// opened connection (postgres/mssql create databases from any open connection).
func (s *remoteSession) CreateDatabase(name string, opts map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return s.p.call(ctx, proto.MethodCreateDB, proto.CreateDBParams{
		ConnID:    s.connID,
		Name:      name,
		Charset:   opts["charset"],
		Collation: opts["collation"],
	}, nil)
}

// pluginQueryTimeout matches the statement cap inside plugins; the host side
// must not give up before the plugin reports its own timeout.
const pluginQueryTimeout = 11 * time.Minute
