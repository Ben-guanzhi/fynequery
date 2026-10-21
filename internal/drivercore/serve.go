package drivercore

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Driver is what a driver-plugin binary implements: open a *sql.DB from a
// connection config. Everything else (query, exec, transactions) is generic and
// lives in Serve.
type Driver interface {
	Open(cfg ConnConfig) (*sql.DB, error)
}

// server is the per-plugin state Serve drives.
type server struct {
	d       Driver
	out     io.Writer
	writeMu sync.Mutex

	stateMu  sync.Mutex
	sessions map[string]*sql.DB
	txs      map[string]*sql.Tx
	cancels  map[int64]context.CancelFunc
	sessSeq  int
	txSeq    int
}

// Serve runs the plugin loop: read a framed request from in, dispatch it
// against the Driver, write a framed reply to out. It returns when in closes
// (the app exited) or a frame fails to decode. A plugin main is just:
//
//	func main() { drivercore.Serve(pgDriver{}, os.Stdin, os.Stdout) }
//
// in and out are parameters rather than os.Stdin/os.Stdout so the whole loop
// can be driven over an in-memory pipe in tests.
//
// Query and exec run on their own goroutine with a cancellable context, so a
// "cancel" request carrying their id can abort a long-running statement while
// it is still in flight - the app-side mirror of ctx cancellation, which cannot
// cross the process boundary on its own. Everything else is handled inline.
func Serve(d Driver, in io.Reader, out io.Writer) error {
	s := &server{
		d:        d,
		out:      out,
		sessions: map[string]*sql.DB{},
		txs:      map[string]*sql.Tx{},
		cancels:  map[int64]context.CancelFunc{},
	}
	reader := bufio.NewReader(in)

	for {
		body, err := readFrame(reader)
		if err != nil {
			if err == io.EOF {
				return nil // the app closed the pipe; a clean shutdown
			}
			return err
		}
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			s.reply(response{ID: req.ID, Error: "bad request: " + err.Error()})
			continue
		}
		if req.Method == methodCancel {
			s.cancel(req)
			continue
		}
		go s.handle(req)
	}
}

func (s *server) reply(resp response) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = writeFrame(s.out, mustJSON(resp))
}

// cancel aborts an in-flight query/exec by its request id.
func (s *server) cancel(req request) {
	var p cancelParams
	_ = json.Unmarshal(req.Params, &p)
	s.stateMu.Lock()
	c, ok := s.cancels[p.ID]
	s.stateMu.Unlock()
	if ok {
		c()
	}
	s.reply(response{ID: req.ID, Result: mustJSON(struct{}{})})
}

// handle runs one non-cancel request and writes its reply.
func (s *server) handle(req request) {
	switch req.Method {
	case methodQuery:
		s.handleQueryStream(req)
		return
	case methodExec:
		s.handleCancellable(req)
		return
	}
	result, rpcErr := s.dispatch(req)
	if rpcErr != nil {
		s.reply(response{ID: req.ID, Error: rpcErr.Error()})
		return
	}
	s.reply(response{ID: req.ID, Result: mustJSON(result)})
}

// replyChunk writes one frame of a streamed resultset.
func (s *server) replyChunk(id int64, chunk queryChunk) {
	s.reply(response{ID: id, Result: mustJSON(chunk)})
}

// handleQueryStream runs a query and streams its rows back in chunks: a head
// frame with the columns/types, then batches of rows, then a Done frame. Each
// batch is freed as it is sent, so a large result never exists as one giant
// frame. The statement's context is cancellable, so a 取消 on the app side stops
// the scan mid-way.
func (s *server) handleQueryStream(req request) {
	ctx, cancel := context.WithCancel(context.Background())
	s.stateMu.Lock()
	s.cancels[req.ID] = cancel
	s.stateMu.Unlock()
	defer func() {
		s.stateMu.Lock()
		delete(s.cancels, req.ID)
		s.stateMu.Unlock()
	}()

	var p queryParams
	_ = json.Unmarshal(req.Params, &p)
	db, ok := s.session(p.Session)
	if !ok {
		s.replyChunk(req.ID, queryChunk{Err: "unknown session", Done: true})
		return
	}
	rows, err := db.QueryContext(ctx, p.Query, p.Args...)
	if err != nil {
		s.replyChunk(req.ID, queryChunk{Err: err.Error(), Done: true})
		return
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		s.replyChunk(req.ID, queryChunk{Err: err.Error(), Done: true})
		return
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		s.replyChunk(req.ID, queryChunk{Err: err.Error(), Done: true})
		return
	}
	names := make([]string, len(cols))
	types := make([]string, len(cols))
	for i := range cols {
		names[i] = cols[i]
		if colTypes[i] != nil {
			types[i] = colTypes[i].DatabaseTypeName()
		}
	}
	s.replyChunk(req.ID, queryChunk{Columns: names, Types: types})

	batch := make([][]cell, 0, queryChunkRows)
	flush := func(done bool, errStr string) {
		s.replyChunk(req.ID, queryChunk{Rows: batch, Done: done, Err: errStr})
		batch = batch[:0]
	}
	values := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			flush(false, "")
			s.replyChunk(req.ID, queryChunk{Err: err.Error(), Done: true})
			return
		}
		row := make([]interface{}, len(values))
		for i, v := range values {
			switch val := v.(type) {
			case []byte:
				row[i] = string(val)
			case time.Time:
				row[i] = val.Format(time.RFC3339)
			default:
				row[i] = val
			}
		}
		batch = append(batch, encodeRow(row))
		if len(batch) >= queryChunkRows {
			flush(false, "")
		}
	}
	endErr := ""
	if err := rows.Err(); err != nil {
		endErr = err.Error()
	}
	flush(true, endErr)
}

// handleCancellable runs a query/exec under a context the cancel method can
// trip, so a 取消 on the app side reaches the driver mid-statement.
func (s *server) handleCancellable(req request) {
	ctx, cancel := context.WithCancel(context.Background())
	s.stateMu.Lock()
	s.cancels[req.ID] = cancel
	s.stateMu.Unlock()
	defer func() {
		s.stateMu.Lock()
		delete(s.cancels, req.ID)
		s.stateMu.Unlock()
	}()

	result, rpcErr := s.dispatchCtx(ctx, req)
	if rpcErr != nil {
		s.reply(response{ID: req.ID, Error: rpcErr.Error()})
		return
	}
	s.reply(response{ID: req.ID, Result: mustJSON(result)})
}

// dispatch runs a non-cancellable request against the current state.
func (s *server) dispatch(req request) (interface{}, error) {
	return s.dispatchCtx(context.Background(), req)
}

// dispatchCtx runs a request with the given context. It fetches the session or
// transaction handle under the state lock, then releases it before the (possibly
// long) database call - database/sql handles are safe for concurrent use.
func (s *server) dispatchCtx(ctx context.Context, req request) (interface{}, error) {
	switch req.Method {
	case methodOpen:
		var p openParams
		_ = json.Unmarshal(req.Params, &p)
		db, err := s.d.Open(p.Config)
		if err != nil {
			return nil, err
		}
		s.stateMu.Lock()
		s.sessSeq++
		id := fmt.Sprintf("s%d", s.sessSeq)
		s.sessions[id] = db
		s.stateMu.Unlock()
		return openResult{Session: id}, nil

	case methodPing:
		var p sessionParams
		_ = json.Unmarshal(req.Params, &p)
		db, ok := s.session(p.Session)
		if !ok {
			return nil, fmt.Errorf("unknown session")
		}
		return struct{}{}, db.PingContext(ctx)

	case methodExec:
		var p execParams
		_ = json.Unmarshal(req.Params, &p)
		db, ok := s.session(p.Session)
		if !ok {
			return nil, fmt.Errorf("unknown session")
		}
		res, err := db.ExecContext(ctx, p.Query, p.Args...)
		if err != nil {
			return execReply{Err: err.Error()}, nil
		}
		aff, _ := res.RowsAffected()
		lid, _ := res.LastInsertId()
		return execReply{Affected: aff, LastInsertID: lid}, nil

	case methodBegin:
		var p sessionParams
		_ = json.Unmarshal(req.Params, &p)
		db, ok := s.session(p.Session)
		if !ok {
			return nil, fmt.Errorf("unknown session")
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		s.stateMu.Lock()
		s.txSeq++
		id := fmt.Sprintf("t%d", s.txSeq)
		s.txs[id] = tx
		s.stateMu.Unlock()
		return beginResult{Tx: id}, nil

	case methodTxExec:
		var p txParams
		_ = json.Unmarshal(req.Params, &p)
		tx, ok := s.tx(p.Tx)
		if !ok {
			return nil, fmt.Errorf("unknown transaction")
		}
		res, err := tx.ExecContext(ctx, p.Query, p.Args...)
		if err != nil {
			return nil, err
		}
		aff, _ := res.RowsAffected()
		return affectedResult{Affected: aff}, nil

	case methodTxCommit:
		var p txOnlyParams
		_ = json.Unmarshal(req.Params, &p)
		tx, ok := s.tx(p.Tx)
		if !ok {
			return nil, fmt.Errorf("unknown transaction")
		}
		err := tx.Commit()
		s.dropTx(p.Tx)
		return struct{}{}, err

	case methodTxRollback:
		var p txOnlyParams
		_ = json.Unmarshal(req.Params, &p)
		tx, ok := s.tx(p.Tx)
		if !ok {
			return nil, fmt.Errorf("unknown transaction")
		}
		err := tx.Rollback()
		s.dropTx(p.Tx)
		return struct{}{}, err

	case methodClose:
		var p sessionParams
		_ = json.Unmarshal(req.Params, &p)
		db, ok := s.session(p.Session)
		if !ok {
			return nil, fmt.Errorf("unknown session")
		}
		err := db.Close()
		s.stateMu.Lock()
		delete(s.sessions, p.Session)
		s.stateMu.Unlock()
		return struct{}{}, err
	}
	return nil, fmt.Errorf("unknown method %q", req.Method)
}

func (s *server) session(id string) (*sql.DB, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	db, ok := s.sessions[id]
	return db, ok
}

func (s *server) tx(id string) (*sql.Tx, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	tx, ok := s.txs[id]
	return tx, ok
}

func (s *server) dropTx(id string) {
	s.stateMu.Lock()
	delete(s.txs, id)
	s.stateMu.Unlock()
}
