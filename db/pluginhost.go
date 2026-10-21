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

// Shutdown kills every spawned plugin process. Called on app exit.
func Shutdown() {
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
	if banner.Proto != proto.ProtocolName || banner.Version != proto.ProtocolVersion {
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
	p.mu.Unlock()
	for _, ch := range waiters {
		ch <- proto.Response{Error: fmt.Sprintf("plugin: %v", err)}
	}
}

func (p *pluginProc) kill() {
	p.mu.Lock()
	dead := p.dead
	p.dead = errors.New("shut down")
	pending := p.pending
	p.pending = map[int64]chan proto.Response{}
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

// ---- Dialer / Session implementation ----

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
	// Best effort: a failed close just leaves the conn to die with the process.
	_ = s.p.call(ctx, proto.MethodClose, proto.CloseParams{ConnID: s.connID}, nil)
}

// pluginQueryTimeout matches the statement cap inside plugins; the host side
// must not give up before the plugin reports its own timeout.
const pluginQueryTimeout = 11 * time.Minute
