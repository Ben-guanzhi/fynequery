package proto

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// Handler implements the plugin side of the protocol. Each method returns a
// JSON-marshalable result or an error; Serve encodes either into a Response.
type Handler interface {
	Meta() MetaResult
	Open(p ConnectParams) (OpenResult, error)
	Close(p CloseParams) error
	Tables(p TablesParams) (TablesResult, error)
	Columns(p ColumnsParams) (ColumnsResult, error)
	Query(p QueryParams) (QueryResult, error)
	Browse(p BrowseParams) (QueryResult, error)
	CreateDatabase(p CreateDBParams) error

	// The four DML methods mirror the editable-grid path of the built-in
	// driver. Update/Insert/Delete apply a single row op; ApplyBatch applies
	// a whole commit atomically. They are additive (see messages.go).
	Update(p DMLSingleParams) (DMLResult, error)
	Insert(p DMLSingleParams) (DMLResult, error)
	Delete(p DMLSingleParams) (DMLResult, error)
	ApplyBatch(p DMLBatchParams) (DMLResult, error)

	// Begin/Commit/Rollback drive a manual transaction on a connection. They
	// are additive (see messages.go).
	Begin(p TxParams) error
	Commit(p TxParams) error
	Rollback(p TxParams) error

	// QueryStream streams a statement's result in batches via emit and returns
	// the aggregate (rows affected / duration) sent as the terminal frame. It
	// is additive (see messages.go).
	QueryStream(p QueryStreamParams, emit func(StreamFrame) error) (QueryResult, error)

	// Metrics returns a curated set of read-only server metrics. Additive (see
	// messages.go).
	Metrics(p MetricsParams) (MetricsResult, error)
}

var stdoutMu sync.Mutex

// writeLine marshals v and writes it as one line to w.
func writeLine(w io.Writer, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// Serve writes the startup banner and then serves requests read from stdin
// until EOF (the host closed stdin).
func Serve(h Handler, banner Banner) error {
	return serve(h, banner, os.Stdin, os.Stdout)
}

// serve is the testable core of Serve: requests from r, responses to w.
func serve(h Handler, banner Banner, r io.Reader, w io.Writer) error {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := writeLine(w, banner); err != nil {
		return fmt.Errorf("write banner: %w", err)
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // SQL can be large
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			// A malformed line cannot be matched to an id; skip it.
			log.Printf("bad request line: %v", err)
			continue
		}
		go func(req Request) {
			res := Response{ID: req.ID}
			if req.Method == MethodQueryStream {
				serveStream(h, req, w)
				return
			}
			result, err := dispatchSafe(h, req)
			if err != nil {
				res.Error = err.Error()
			} else {
				b, jerr := json.Marshal(result)
				if jerr != nil {
					res.Error = fmt.Sprintf("marshal result: %v", jerr)
				} else {
					res.Result = b
				}
			}
			if werr := writeLine(w, res); werr != nil {
				log.Printf("write response: %v", werr)
				os.Exit(1)
			}
		}(req)
	}
	return sc.Err()
}

// dispatchSafe isolates handler panics: a panicking method surfaces as an
// error response for that one request instead of killing the plugin process
// (the host would otherwise see a broken pipe on every later call too).
func dispatchSafe(h Handler, req Request) (result interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin panic: %v", r)
		}
	}()
	return dispatch(h, req)
}

// call unmarshals params into P and invokes fn. The unmarshal-and-call shape
// was duplicated once per method in dispatch's switch; the table below keeps
// each method a single line instead.
func call[P any, R any](h Handler, params json.RawMessage, fn func(P) (R, error)) (any, error) {
	var q P
	if len(params) > 0 {
		if err := json.Unmarshal(params, &q); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
	}
	return fn(q)
}

// callVoid is call for methods that return only an error.
func callVoid[P any](h Handler, params json.RawMessage, fn func(P) error) (any, error) {
	var q P
	if len(params) > 0 {
		if err := json.Unmarshal(params, &q); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
	}
	return struct{}{}, fn(q)
}

// dispatchTable maps one protocol method to its handler. Add a row here when
// the Handler interface grows a method.
var dispatchTable = map[string]func(Handler, json.RawMessage) (any, error){
	MethodMeta:      func(h Handler, _ json.RawMessage) (any, error) { return h.Meta(), nil },
	MethodOpen:      func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.Open) },
	MethodClose:     func(h Handler, p json.RawMessage) (any, error) { return callVoid(h, p, h.Close) },
	MethodTables:    func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.Tables) },
	MethodColumns:   func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.Columns) },
	MethodQuery:     func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.Query) },
	MethodBrowse:    func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.Browse) },
	MethodCreateDB:  func(h Handler, p json.RawMessage) (any, error) { return callVoid(h, p, h.CreateDatabase) },
	MethodUpdate:    func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.Update) },
	MethodInsert:    func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.Insert) },
	MethodDelete:    func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.Delete) },
	MethodApplyBatch: func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.ApplyBatch) },
	MethodBegin:     func(h Handler, p json.RawMessage) (any, error) { return callVoid(h, p, h.Begin) },
	MethodCommit:    func(h Handler, p json.RawMessage) (any, error) { return callVoid(h, p, h.Commit) },
	MethodRollback:  func(h Handler, p json.RawMessage) (any, error) { return callVoid(h, p, h.Rollback) },
	MethodMetrics:   func(h Handler, p json.RawMessage) (any, error) { return call(h, p, h.Metrics) },
}

func dispatch(h Handler, req Request) (interface{}, error) {
	if fn, ok := dispatchTable[req.Method]; ok {
		return fn(h, req.Params)
	}
	return nil, fmt.Errorf("unknown method %q", req.Method)
}

// serveStream runs a MethodQueryStream request: it writes one Response frame
// per batch the handler emits, then a terminal frame (Response.Done) carrying
// the aggregate result or the error.
func serveStream(h Handler, req Request, w io.Writer) {
	var p QueryStreamParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = writeLine(w, Response{ID: req.ID, Error: fmt.Sprintf("bad params: %v", err), Done: true})
		return
	}
	emit := func(f StreamFrame) error {
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		return writeLine(w, Response{ID: req.ID, Result: b})
	}
	final, err := queryStreamSafe(h, p, emit)
	if err != nil {
		_ = writeLine(w, Response{ID: req.ID, Error: err.Error(), Done: true})
		return
	}
	b, err := json.Marshal(final)
	if err != nil {
		_ = writeLine(w, Response{ID: req.ID, Error: fmt.Sprintf("marshal result: %v", err), Done: true})
		return
	}
	_ = writeLine(w, Response{ID: req.ID, Result: b, Done: true})
}

// queryStreamSafe runs a streaming handler with the same panic isolation as
// dispatchSafe: the panic becomes the terminal error frame, the process lives.
func queryStreamSafe(h Handler, p QueryStreamParams, emit func(StreamFrame) error) (final QueryResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin panic: %v", r)
		}
	}()
	return h.QueryStream(p, emit)
}
