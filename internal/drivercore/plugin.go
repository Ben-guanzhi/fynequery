package drivercore

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Wire protocol between the app and a driver plugin. Every message is a
// 4-byte big-endian length followed by that many JSON bytes, on the child's
// stdin (requests) and stdout (replies). Requests carry an id; replies echo it.
//
// The app keeps one outstanding database call at a time, but a query or exec can
// be cancelled mid-flight: the app sends the statement, and if its context is
// cancelled it sends a "cancel" request carrying the statement's id, which the
// plugin uses to trip that statement's context. So the transport is asynchronous
// - a reader goroutine routes each reply to the waiter registered for its id -
// rather than a blocking read-per-call.
const (
	methodOpen       = "open"
	methodPing       = "ping"
	methodQuery      = "query"
	methodExec       = "exec"
	methodBegin      = "begin"
	methodTxExec     = "txExec"
	methodTxCommit   = "txCommit"
	methodTxRollback = "txRollback"
	methodClose      = "close"
	methodCancel     = "cancel"
)

type request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type openParams struct {
	Config ConnConfig `json:"config"`
}

type openResult struct {
	Session string `json:"session"`
}

type sessionParams struct {
	Session string `json:"session"`
}

type queryParams struct {
	Session string        `json:"session"`
	Query   string        `json:"query"`
	Args    []interface{} `json:"args,omitempty"`
}

type execParams struct {
	Session string        `json:"session"`
	Query   string        `json:"query"`
	Args    []interface{} `json:"args,omitempty"`
}

type beginResult struct {
	Tx string `json:"tx"`
}

type txParams struct {
	Tx    string        `json:"tx"`
	Query string        `json:"query"`
	Args  []interface{} `json:"args,omitempty"`
}

type txOnlyParams struct {
	Tx string `json:"tx"`
}

type affectedResult struct {
	Affected int64 `json:"affected"`
}

// cancelParams names the in-flight statement (by its request id) to abort.
type cancelParams struct {
	ID int64 `json:"id"`
}

// cancelDrainTimeout bounds how long callCtx waits for the aborted statement's
// reply after a cancel, so a plugin that died mid-cancel cannot hang the caller.
const cancelDrainTimeout = 5 * time.Second

// pluginProcess is one running driver-plugin child with its framed stdio
// channel. A reader goroutine routes replies to per-id waiters so a call can
// wait with a context and still fire a cancel while a statement runs.
type pluginProcess struct {
	w       io.Writer
	r       *bufio.Reader
	closer  io.Closer
	cmd     *exec.Cmd
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	waiters map[int64]chan response
	streams map[int64]chan queryChunk
	dead    bool
}

func startPlugin(binary string) (*pluginProcess, error) {
	cmd := exec.Command(binary)
	cmd.Stderr = nil // a chatty plugin must not corrupt the framed stdout
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start driver plugin %s: %w", binary, err)
	}
	p := newPluginProcessConn(stdout, stdin, stdin)
	p.cmd = cmd
	return p, nil
}

// newPluginProcessConn wires a pluginProcess to an already-connected reader and
// writer, with no subprocess, and starts its reader goroutine. It is what the
// round-trip test uses to drive Serve and the client over an in-memory pipe.
func newPluginProcessConn(r io.Reader, w io.Writer, closer io.Closer) *pluginProcess {
	p := &pluginProcess{
		w:       w,
		r:       bufio.NewReader(r),
		closer:  closer,
		waiters: map[int64]chan response{},
		streams: map[int64]chan queryChunk{},
	}
	go p.readLoop()
	return p
}

// readLoop reads replies and hands each to its waiter. A reply with no waiter
// (a cancel's own ack, say) is dropped. On a read error it marks the process
// dead and fails every outstanding waiter so no caller blocks forever.
func (p *pluginProcess) readLoop() {
	for {
		body, err := readFrame(p.r)
		if err != nil {
			p.failWaiters(err)
			return
		}
		var resp response
		if err := json.Unmarshal(body, &resp); err != nil {
			p.failWaiters(fmt.Errorf("decode driver plugin reply: %w", err))
			return
		}
		// A streamed query delivers many frames under one id; route those to
		// the stream channel, everything else to a one-shot waiter.
		p.mu.Lock()
		sch, isStream := p.streams[resp.ID]
		if isStream && (resp.Error != "" || chunkIsDone(resp.Result)) {
			delete(p.streams, resp.ID)
		}
		ch, isWaiter := p.waiters[resp.ID]
		if isWaiter {
			delete(p.waiters, resp.ID)
		}
		p.mu.Unlock()
		if isStream {
			sch <- streamFrameOf(resp) // buffered, never blocks
			continue
		}
		if isWaiter {
			ch <- resp
		}
	}
}

// chunkIsDone reports whether a stream frame's payload marks the end.
func chunkIsDone(result json.RawMessage) bool {
	if len(result) == 0 {
		return true // an error reply with no payload ends the stream
	}
	var c queryChunk
	if json.Unmarshal(result, &c) != nil {
		return true
	}
	return c.Done
}

// streamFrameOf turns a reply into a chunk, folding a transport-level error into
// the chunk's Err so the consumer sees it as a stream error.
func streamFrameOf(resp response) queryChunk {
	var c queryChunk
	if len(resp.Result) > 0 {
		_ = json.Unmarshal(resp.Result, &c)
	}
	if resp.Error != "" {
		c.Err = resp.Error
		c.Done = true
	}
	return c
}

func (p *pluginProcess) failWaiters(cause error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return
	}
	p.dead = true
	msg := "driver plugin stopped"
	if cause != nil && cause != io.EOF {
		msg = "driver plugin connection lost: " + cause.Error()
	}
	for id, ch := range p.waiters {
		ch <- response{ID: id, Error: msg}
		delete(p.waiters, id)
	}
	for id, sch := range p.streams {
		sch <- queryChunk{Err: msg, Done: true}
		delete(p.streams, id)
	}
}

// send writes a request and returns its id plus the channel its reply will
// arrive on.
func (p *pluginProcess) send(method string, params interface{}) (int64, chan response, error) {
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return 0, nil, errors.New("driver plugin is not running")
	}
	id := p.nextID
	p.nextID++
	ch := make(chan response, 1)
	p.waiters[id] = ch
	p.mu.Unlock()

	var raw json.RawMessage
	if params != nil {
		raw = mustJSON(params)
	}
	p.writeMu.Lock()
	err := writeFrame(p.w, mustJSON(request{ID: id, Method: method, Params: raw}))
	p.writeMu.Unlock()
	if err != nil {
		p.dropWaiter(id)
		return 0, nil, fmt.Errorf("write to driver plugin: %w", err)
	}
	return id, ch, nil
}

func (p *pluginProcess) dropWaiter(id int64) {
	p.mu.Lock()
	delete(p.waiters, id)
	p.mu.Unlock()
}

// sendStream starts a streamed request (a query) and returns its id plus the
// channel its chunk frames arrive on. The channel is buffered so the reader
// goroutine never blocks on a slow consumer; flow control comes from the pipe.
func (p *pluginProcess) sendStream(method string, params interface{}) (int64, chan queryChunk, error) {
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return 0, nil, errors.New("driver plugin is not running")
	}
	id := p.nextID
	p.nextID++
	ch := make(chan queryChunk, 4)
	p.streams[id] = ch
	p.mu.Unlock()

	var raw json.RawMessage
	if params != nil {
		raw = mustJSON(params)
	}
	p.writeMu.Lock()
	err := writeFrame(p.w, mustJSON(request{ID: id, Method: method, Params: raw}))
	p.writeMu.Unlock()
	if err != nil {
		p.dropStream(id)
		return 0, nil, fmt.Errorf("write to driver plugin: %w", err)
	}
	return id, ch, nil
}

func (p *pluginProcess) dropStream(id int64) {
	p.mu.Lock()
	delete(p.streams, id)
	p.mu.Unlock()
}

// call runs a request and blocks for its reply. Use it for the fast, non-
// cancellable methods (open, ping, begin, tx, close).
func (p *pluginProcess) call(method string, params interface{}, result interface{}) error {
	_, ch, err := p.send(method, params)
	if err != nil {
		return err
	}
	return decodeReply(<-ch, result)
}

// callCtx runs a cancellable request: if ctx is cancelled first, it sends a
// cancel for the statement's id and then waits for the (error) reply the driver
// produces once the statement is aborted.
func (p *pluginProcess) callCtx(ctx context.Context, method string, params interface{}, result interface{}) error {
	id, ch, err := p.send(method, params)
	if err != nil {
		return err
	}
	select {
	case resp := <-ch:
		return decodeReply(resp, result)
	case <-ctx.Done():
		p.sendCancel(id)
		// The statement is being torn down; its reply will be an error. Wait for
		// it so the connection is left clean, but do not block forever if the
		// plugin died.
		select {
		case resp := <-ch:
			return decodeReply(resp, result)
		case <-time.After(cancelDrainTimeout):
			return ctx.Err()
		}
	}
}

// sendCancel asks the plugin to abort the in-flight statement with this id. Its
// own ack is not awaited, so no waiter is registered for it.
func (p *pluginProcess) sendCancel(id int64) {
	p.mu.Lock()
	dead := p.dead
	p.mu.Unlock()
	if dead {
		return
	}
	p.writeMu.Lock()
	_ = writeFrame(p.w, mustJSON(request{Method: methodCancel, Params: mustJSON(cancelParams{ID: id})}))
	p.writeMu.Unlock()
}

func decodeReply(resp response, result interface{}) error {
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("decode driver plugin result: %w", err)
		}
	}
	return nil
}

func (p *pluginProcess) stop() {
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return
	}
	p.dead = true
	p.mu.Unlock()
	if p.closer != nil {
		p.closer.Close()
	}
	if p.cmd != nil {
		p.cmd.Wait()
	}
}

func writeFrame(w io.Writer, b []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// NewPlugin spawns the driver plugin binary and opens a session on it. The
// caller (the db package) has already resolved binary from the plugins
// directory for cfg.Type.
func NewPlugin(binary string, cfg ConnConfig) (Engine, error) {
	proc, err := startPlugin(binary)
	if err != nil {
		return nil, err
	}
	var res openResult
	if err := proc.call(methodOpen, openParams{Config: cfg}, &res); err != nil {
		proc.stop()
		return nil, err
	}
	return &pluginEngine{proc: proc, session: res.Session}, nil
}

type pluginEngine struct {
	proc    *pluginProcess
	session string
}

func (e *pluginEngine) Query(ctx context.Context, query string, args ...interface{}) (RowSet, error) {
	id, ch, err := e.proc.sendStream(methodQuery, queryParams{Session: e.session, Query: query, Args: args})
	if err != nil {
		return nil, err
	}
	var (
		cols  []string
		types []string
		rows  [][]interface{}
	)
	for {
		select {
		case chunk := <-ch:
			if len(chunk.Columns) > 0 {
				cols, types = chunk.Columns, chunk.Types
			}
			for _, c := range chunk.Rows {
				rows = append(rows, decodeRow(c))
			}
			if chunk.Err != "" {
				return nil, errors.New(chunk.Err)
			}
			if chunk.Done {
				// The grid needs the whole set in memory to render, so the
				// client still assembles every row - but it arrived in bounded
				// chunks, never as one giant frame, and the plugin freed each
				// batch as it sent it.
				return &materializedRowSet{cols: cols, types: types, rows: rows}, nil
			}
		case <-ctx.Done():
			e.proc.sendCancel(id)
			e.proc.dropStream(id)
			return nil, ctx.Err()
		}
	}
}

func (e *pluginEngine) Exec(ctx context.Context, query string, args ...interface{}) (ExecResult, error) {
	var rep execReply
	if err := e.proc.callCtx(ctx, methodExec, execParams{Session: e.session, Query: query, Args: args}, &rep); err != nil {
		return ExecResult{}, err
	}
	if rep.Err != "" {
		return ExecResult{}, errors.New(rep.Err)
	}
	return ExecResult{Affected: rep.Affected, LastInsertID: rep.LastInsertID}, nil
}

func (e *pluginEngine) Begin(ctx context.Context) (Tx, error) {
	var res beginResult
	if err := e.proc.call(methodBegin, sessionParams{Session: e.session}, &res); err != nil {
		return nil, err
	}
	return &pluginTx{proc: e.proc, tx: res.Tx}, nil
}

func (e *pluginEngine) Ping(ctx context.Context) error {
	return e.proc.call(methodPing, sessionParams{Session: e.session}, nil)
}

func (e *pluginEngine) Close() error {
	err := e.proc.call(methodClose, sessionParams{Session: e.session}, nil)
	e.proc.stop()
	return err
}

type pluginTx struct {
	proc *pluginProcess
	tx   string
}

func (t *pluginTx) Exec(ctx context.Context, query string, args ...interface{}) (int64, error) {
	var res affectedResult
	if err := t.proc.callCtx(ctx, methodTxExec, txParams{Tx: t.tx, Query: query, Args: args}, &res); err != nil {
		return 0, err
	}
	return res.Affected, nil
}

func (t *pluginTx) Commit(ctx context.Context) error {
	return t.proc.call(methodTxCommit, txOnlyParams{Tx: t.tx}, nil)
}

func (t *pluginTx) Rollback(ctx context.Context) error {
	return t.proc.call(methodTxRollback, txOnlyParams{Tx: t.tx}, nil)
}
