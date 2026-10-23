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
			result, err := dispatch(h, req)
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

func dispatch(h Handler, req Request) (interface{}, error) {
	switch req.Method {
	case MethodMeta:
		return h.Meta(), nil
	case MethodOpen:
		var p ConnectParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.Open(p)
	case MethodClose:
		var p CloseParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return struct{}{}, h.Close(p)
	case MethodTables:
		var p TablesParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.Tables(p)
	case MethodColumns:
		var p ColumnsParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.Columns(p)
	case MethodQuery:
		var p QueryParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.Query(p)
	case MethodBrowse:
		var p BrowseParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.Browse(p)
	case MethodCreateDB:
		var p CreateDBParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return struct{}{}, h.CreateDatabase(p)
	case MethodUpdate:
		var p DMLSingleParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.Update(p)
	case MethodInsert:
		var p DMLSingleParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.Insert(p)
	case MethodDelete:
		var p DMLSingleParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.Delete(p)
	case MethodApplyBatch:
		var p DMLBatchParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.ApplyBatch(p)
	case MethodBegin:
		var p TxParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return struct{}{}, h.Begin(p)
	case MethodCommit:
		var p TxParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return struct{}{}, h.Commit(p)
	case MethodRollback:
		var p TxParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return struct{}{}, h.Rollback(p)
	case MethodMetrics:
		var p MetricsParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return h.Metrics(p)
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
	final, err := h.QueryStream(p, emit)
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
