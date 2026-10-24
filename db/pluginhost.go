package db

// Plugin host: discovers plugin executables next to the app, spawns them as
// hidden child processes and exposes each one as a Dialer over the stdin/
// stdout JSON protocol defined in fynequery/proto.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fynequery/proto"
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

// pluginProc is one spawned plugin executable acting as a Dialer.
type pluginProc struct {
	info   DriverInfo
	exe    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan proto.Response
	streams map[int64]chan proto.Response // in-flight streaming calls (multi-frame)
	dead    error
}

func spawnPlugin(exe string) (*pluginProc, error) {
	cmd := exec.Command(exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", exe, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", exe, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", exe, err)
	}
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", exe, err)
	}

	p := &pluginProc{
		exe:     exe,
		cmd:     cmd,
		stdin:   stdin,
		reader:  bufio.NewReader(stdout),
		pending: map[int64]chan proto.Response{},
		streams: map[int64]chan proto.Response{},
	}

	// Plugin diagnostics (stderr) go to the host log, prefixed.
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			logf("[%s] %s", filepath.Base(exe), sc.Text())
		}
	}()

	if err := p.handshake(); err != nil {
		p.kill()
		return nil, fmt.Errorf("%s: %w", exe, err)
	}

	go func() {
		err := cmd.Wait()
		if err == nil {
			err = errors.New("plugin process exited")
		}
		p.failAll(err)
	}()
	return p, nil
}

// handshake reads the startup banner and exchanges meta for driver info.
func (p *pluginProc) handshake() error {
	bannerCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := p.reader.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		bannerCh <- line
	}()
	var banner proto.Banner
	select {
	case line := <-bannerCh:
		if err := json.Unmarshal([]byte(line), &banner); err != nil {
			return fmt.Errorf("bad banner: %w", err)
		}
	case err := <-errCh:
		return fmt.Errorf("read banner: %w", err)
	case <-time.After(5 * time.Second):
		return errors.New("banner timeout")
	}
	// Both protocol names are accepted: the kernel's and the legacy fynequery one
	// the deployed plugin binaries still announce. The version must match either
	// way — a different version means a different framing.
	if err := proto.AcceptsBanner(banner); err != nil {
		return err
	}
	if banner.Version != proto.ProtocolVersion {
		return fmt.Errorf("unsupported protocol %q v%d", banner.Proto, banner.Version)
	}

	// The response reader must be running before the first round trip,
	// otherwise the meta response would sit unread.
	go p.readLoop()

	var meta proto.MetaResult
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.call(ctx, proto.MethodMeta, nil, &meta); err != nil {
		return fmt.Errorf("meta: %w", err)
	}
	if meta.Code == "" {
		return errors.New("plugin reported empty driver code")
	}
	p.info = DriverInfo{
		Code:        meta.Code,
		Name:        meta.Name,
		Version:     meta.Version,
		DefaultPort: meta.DefaultPort,
		Plugin:      true,
	}
	return nil
}

// readLoop dispatches responses to waiting callers until the pipe breaks.
func (p *pluginProc) readLoop() {
	for {
		line, err := p.reader.ReadBytes('\n')
		if len(line) > 0 {
			var resp proto.Response
			if jerr := json.Unmarshal(line, &resp); jerr == nil {
				p.mu.Lock()
				if sc, ok := p.streams[resp.ID]; ok {
					// Streaming frame: keep the channel open until the terminal
					// (Done) frame, then retire it.
					if resp.Done {
						delete(p.streams, resp.ID)
					}
					p.mu.Unlock()
					sc <- resp
					continue
				}
				ch := p.pending[resp.ID]
				delete(p.pending, resp.ID)
				p.mu.Unlock()
				if ch != nil {
					ch <- resp
				}
				continue
			}
		}
		if err != nil {
			if err == io.EOF {
				err = errors.New("plugin process exited")
			}
			p.failAll(err)
			return
		}
	}
}

// failAll wakes every waiter and marks the process unusable.
func (p *pluginProc) failAll(err error) {
	p.mu.Lock()
	if p.dead == nil {
		p.dead = err
	}
	waiters := make([]chan proto.Response, 0, len(p.pending))
	for id, ch := range p.pending {
		waiters = append(waiters, ch)
		delete(p.pending, id)
	}
	streams := make([]chan proto.Response, 0, len(p.streams))
	for id, ch := range p.streams {
		streams = append(streams, ch)
		delete(p.streams, id)
	}
	p.mu.Unlock()
	for _, ch := range waiters {
		ch <- proto.Response{Error: fmt.Sprintf("plugin: %v", err)}
	}
	for _, ch := range streams {
		ch <- proto.Response{Error: fmt.Sprintf("plugin: %v", err), Done: true}
	}
}

func (p *pluginProc) kill() {
	p.mu.Lock()
	dead := p.dead
	p.dead = errors.New("shut down")
	pending := p.pending
	p.pending = map[int64]chan proto.Response{}
	streams := p.streams
	p.streams = map[int64]chan proto.Response{}
	p.mu.Unlock()
	if dead != nil {
		return // already exited or killed
	}
	if p.stdin != nil {
		p.stdin.Close() // graceful: plugin exits on stdin EOF
	}
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	for _, ch := range pending {
		ch <- proto.Response{Error: "plugin: shut down"}
	}
	for _, ch := range streams {
		ch <- proto.Response{Error: "plugin: shut down", Done: true}
	}
}

// call performs one request/response round trip. params may be nil.
func (p *pluginProc) call(ctx context.Context, method string, params, result interface{}) error {
	p.mu.Lock()
	if p.dead != nil {
		err := fmt.Errorf("plugin: %w", p.dead)
		p.mu.Unlock()
		return err
	}
	id := p.nextID
	p.nextID++
	req := proto.Request{ID: id, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			p.mu.Unlock()
			return err
		}
		req.Params = raw
	}
	line, err := json.Marshal(req)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	ch := make(chan proto.Response, 1)
	p.pending[id] = ch
	_, werr := p.stdin.Write(append(line, '\n'))
	p.mu.Unlock()
	if werr != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return fmt.Errorf("write to plugin: %w", werr)
	}

	select {
	case resp := <-ch:
		if resp.Error != "" {
			return errors.New(resp.Error)
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return ctx.Err()
	}
}

// callStream performs a streaming request/response exchange. It registers the
// request as a stream, sends it, then delivers every frame to onFrame until the
// terminal (Done) frame, whose Result is decoded into final. onFrame must not
// block indefinitely; a false return stops early (the stream is retired).
func (p *pluginProc) callStream(ctx context.Context, method string, params interface{}, onFrame func(proto.StreamFrame) (bool, error), final interface{}) error {
	p.mu.Lock()
	if p.dead != nil {
		err := fmt.Errorf("plugin: %w", p.dead)
		p.mu.Unlock()
		return err
	}
	id := p.nextID
	p.nextID++
	req := proto.Request{ID: id, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			p.mu.Unlock()
			return err
		}
		req.Params = raw
	}
	line, err := json.Marshal(req)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	ch := make(chan proto.Response, 8)
	p.streams[id] = ch
	_, werr := p.stdin.Write(append(line, '\n'))
	p.mu.Unlock()
	if werr != nil {
		p.mu.Lock()
		delete(p.streams, id)
		p.mu.Unlock()
		return fmt.Errorf("write to plugin: %w", werr)
	}

	stop := func() {
		p.mu.Lock()
		delete(p.streams, id)
		p.mu.Unlock()
	}
	for {
		select {
		case resp := <-ch:
			if resp.Error != "" {
				stop()
				return errors.New(resp.Error)
			}
			var f proto.StreamFrame
			if len(resp.Result) > 0 {
				if jerr := json.Unmarshal(resp.Result, &f); jerr != nil {
					stop()
					return jerr
				}
			}
			keep := true
			if !resp.Done && onFrame != nil {
				var e error
				keep, e = onFrame(f)
				if e != nil {
					stop()
					return e
				}
			}
			if resp.Done || !keep {
				stop()
				if resp.Done && final != nil && len(resp.Result) > 0 {
					return json.Unmarshal(resp.Result, final)
				}
				return nil
			}
		case <-ctx.Done():
			stop()
			return ctx.Err()
		}
	}
}

func (p *pluginProc) Info() DriverInfo { return p.info }

func (p *pluginProc) Connect(cfg proto.ConnectParams) (Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var res proto.OpenResult
	if err := p.call(ctx, proto.MethodOpen, cfg, &res); err != nil {
		return nil, fmt.Errorf("%s: %w", p.info.Name, err)
	}
	return &remoteSession{p: p, connID: res.ConnID, server: res.Server}, nil
}

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
