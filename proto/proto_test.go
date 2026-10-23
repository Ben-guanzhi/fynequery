package proto

// Round-trip test of the serve loop over in-memory pipes: a client writes
// request lines and reads responses exactly like the host does.

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type testHandler struct{}

func (testHandler) Meta() MetaResult {
	return MetaResult{Code: "t", Name: "TestDB", Version: "1", DefaultPort: "1"}
}

func (testHandler) Open(p ConnectParams) (OpenResult, error) {
	if p.Host == "fail" {
		return OpenResult{}, errors.New("dial refused")
	}
	return OpenResult{ConnID: "c1", Server: "TestDB 9"}, nil
}

func (testHandler) Close(p CloseParams) error {
	if p.ConnID != "c1" {
		return errors.New("unknown conn")
	}
	return nil
}

func (testHandler) Tables(p TablesParams) (TablesResult, error) {
	if p.ConnID != "c1" {
		return TablesResult{}, errors.New("unknown conn")
	}
	return TablesResult{Objects: []ObjectInfo{{Name: "a", Kind: "table", Rows: 3, ApproxRows: true}}}, nil
}

func (testHandler) Columns(p ColumnsParams) (ColumnsResult, error) {
	return ColumnsResult{Columns: []ColumnInfo{{Name: "id", Type: "int", Key: "PK"}, {Name: "v", Type: "text", Nullable: true}}}, nil
}

func (testHandler) Query(p QueryParams) (QueryResult, error) {
	return QueryResult{
		Columns: []ColumnInfo{{Name: "id"}},
		Rows:    [][]*string{{ptr("1")}, {nil}, {ptr("3")}},
	}, nil
}

func (testHandler) Browse(p BrowseParams) (QueryResult, error) {
	return QueryResult{Columns: []ColumnInfo{{Name: "id"}}, Rows: [][]*string{{ptr("1")}}}, nil
}

func (testHandler) CreateDatabase(p CreateDBParams) error {
	if p.Name == "fail" {
		return errors.New("create refused")
	}
	return nil
}

func (testHandler) Update(p DMLSingleParams) (DMLResult, error) {
	return DMLResult{}, nil
}

func (testHandler) Insert(p DMLSingleParams) (DMLResult, error) {
	return DMLResult{}, nil
}

func (testHandler) Delete(p DMLSingleParams) (DMLResult, error) {
	return DMLResult{}, nil
}

func (testHandler) ApplyBatch(p DMLBatchParams) (DMLResult, error) {
	return DMLResult{}, nil
}

func (testHandler) Begin(p TxParams) error    { return nil }
func (testHandler) Commit(p TxParams) error   { return nil }
func (testHandler) Rollback(p TxParams) error { return nil }

func (testHandler) QueryStream(p QueryStreamParams, emit func(StreamFrame) error) (QueryResult, error) {
	return QueryResult{}, nil
}

func (testHandler) Metrics(p MetricsParams) (MetricsResult, error) {
	return MetricsResult{Metrics: []Metric{{Label: "Uptime", Value: "1h"}}}, nil
}

func ptr(s string) *string { return &s }

// client is the host side of the pipe pair.
type client struct {
	w   io.Writer
	r   *bufio.Reader
	err error
}

func (c *client) call(id int64, method string, params interface{}) (Response, error) {
	raw, _ := json.Marshal(params)
	req, _ := json.Marshal(Request{ID: id, Method: method, Params: raw})
	if _, err := c.w.Write(append(req, '\n')); err != nil {
		return Response{}, err
	}
	line, err := c.r.ReadString('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	return resp, json.Unmarshal([]byte(line), &resp)
}

func TestServeRoundTrip(t *testing.T) {
	// host -> plugin (requests) and plugin -> host (responses)
	reqR, reqW := io.Pipe()
	respR, respW := io.Pipe()

	go func() { _ = serve(testHandler{}, Banner{Proto: ProtocolName, Version: 1}, reqR, respW) }()

	c := &client{w: reqW, r: bufio.NewReader(respR)}

	// banner
	c.r.ReadString('\n') // consume banner line
	resp, err := c.call(1, MethodMeta, nil)
	if err != nil || resp.ID != 1 || resp.Error != "" {
		t.Fatalf("meta: resp=%+v err=%v", resp, err)
	}
	var meta MetaResult
	if err := json.Unmarshal(resp.Result, &meta); err != nil || meta.Code != "t" {
		t.Fatalf("meta result: %+v err=%v", meta, err)
	}

	resp, err = c.call(2, MethodOpen, ConnectParams{Host: "fail"})
	if err != nil || resp.Error == "" || !strings.Contains(resp.Error, "dial refused") {
		t.Fatalf("open failure: resp=%+v err=%v", resp, err)
	}

	resp, err = c.call(3, MethodOpen, ConnectParams{Host: "ok"})
	if err != nil || resp.Error != "" {
		t.Fatalf("open: resp=%+v err=%v", resp, err)
	}
	var open OpenResult
	_ = json.Unmarshal(resp.Result, &open)

	resp, err = c.call(4, MethodTables, TablesParams{ConnID: open.ConnID})
	if err != nil || resp.Error != "" {
		t.Fatalf("tables: resp=%+v err=%v", resp, err)
	}
	var tables TablesResult
	_ = json.Unmarshal(resp.Result, &tables)
	if len(tables.Objects) != 1 || tables.Objects[0].Name != "a" {
		t.Fatalf("tables result: %+v", tables)
	}

	resp, err = c.call(5, MethodQuery, QueryParams{ConnID: open.ConnID, SQL: "SELECT 1"})
	if err != nil || resp.Error != "" {
		t.Fatalf("query: resp=%+v err=%v", resp, err)
	}
	var qr QueryResult
	_ = json.Unmarshal(resp.Result, &qr)
	if len(qr.Rows) != 3 || qr.Rows[1][0] != nil {
		t.Fatalf("query result rows (NULL round trip): %+v", qr.Rows)
	}

	resp, err = c.call(6, MethodClose, CloseParams{ConnID: "bogus"})
	if err != nil || resp.Error == "" {
		t.Fatalf("close bogus: resp=%+v err=%v", resp, err)
	}
}

// streamTestHandler emits a fixed set of frames for MethodQueryStream.
type streamTestHandler struct{}

func (streamTestHandler) Meta() MetaResult { return MetaResult{Code: "s"} }
func (streamTestHandler) Open(ConnectParams) (OpenResult, error) {
	return OpenResult{ConnID: "c1"}, nil
}
func (streamTestHandler) Close(CloseParams) error                      { return nil }
func (streamTestHandler) Tables(TablesParams) (TablesResult, error)    { return TablesResult{}, nil }
func (streamTestHandler) Columns(ColumnsParams) (ColumnsResult, error) { return ColumnsResult{}, nil }
func (streamTestHandler) Query(QueryParams) (QueryResult, error)       { return QueryResult{}, nil }
func (streamTestHandler) Browse(BrowseParams) (QueryResult, error)     { return QueryResult{}, nil }
func (streamTestHandler) CreateDatabase(CreateDBParams) error          { return nil }
func (streamTestHandler) Update(DMLSingleParams) (DMLResult, error)    { return DMLResult{}, nil }
func (streamTestHandler) Insert(DMLSingleParams) (DMLResult, error)    { return DMLResult{}, nil }
func (streamTestHandler) Delete(DMLSingleParams) (DMLResult, error)    { return DMLResult{}, nil }
func (streamTestHandler) ApplyBatch(DMLBatchParams) (DMLResult, error) { return DMLResult{}, nil }
func (streamTestHandler) Begin(TxParams) error                         { return nil }
func (streamTestHandler) Commit(TxParams) error                        { return nil }
func (streamTestHandler) Rollback(TxParams) error                      { return nil }
func (streamTestHandler) Metrics(MetricsParams) (MetricsResult, error) { return MetricsResult{}, nil }

func (streamTestHandler) QueryStream(p QueryStreamParams, emit func(StreamFrame) error) (QueryResult, error) {
	if p.SQL == "boom" {
		// Emit one frame, then fail: the host must see the error as terminal.
		_ = emit(StreamFrame{Columns: []ColumnInfo{{Name: "n"}}, Rows: [][]*string{{ptr("1")}}})
		return QueryResult{}, errors.New("stream blew up")
	}
	if err := emit(StreamFrame{Columns: []ColumnInfo{{Name: "n"}}, Rows: [][]*string{{ptr("1")}, {ptr("2")}}}); err != nil {
		return QueryResult{}, err
	}
	if err := emit(StreamFrame{Rows: [][]*string{{ptr("3")}}}); err != nil {
		return QueryResult{}, err
	}
	return QueryResult{RowsAffected: 0, DurationMS: 7}, nil
}

func TestServeStreamRoundTrip(t *testing.T) {
	reqR, reqW := io.Pipe()
	respR, respW := io.Pipe()
	go func() { _ = serve(streamTestHandler{}, Banner{Proto: ProtocolName, Version: 1}, reqR, respW) }()

	c := &client{w: reqW, r: bufio.NewReader(respR)}
	c.r.ReadString('\n') // banner

	// Send the streaming request, then read frames until the terminal one.
	raw, _ := json.Marshal(QueryStreamParams{ConnID: "c1", SQL: "SELECT n", Batch: 2})
	req, _ := json.Marshal(Request{ID: 42, Method: MethodQueryStream, Params: raw})
	if _, err := c.w.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}

	var gotRows []string
	var cols []ColumnInfo
	var final QueryResult
	frames := 0
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		if resp.ID != 42 {
			t.Fatalf("frame id = %d, want 42", resp.ID)
		}
		var f StreamFrame
		_ = json.Unmarshal(resp.Result, &f)
		if len(f.Columns) > 0 {
			cols = f.Columns
		}
		for _, r := range f.Rows {
			gotRows = append(gotRows, *r[0])
		}
		frames++
		if resp.Done {
			final = QueryResult{RowsAffected: f.RowsAffected, DurationMS: f.DurationMS}
			if resp.Error != "" {
				t.Fatalf("terminal frame carried error: %s", resp.Error)
			}
			break
		}
	}
	if frames != 3 {
		t.Fatalf("got %d frames, want 3", frames)
	}
	if len(cols) != 1 || cols[0].Name != "n" {
		t.Fatalf("columns = %+v", cols)
	}
	if strings.Join(gotRows, ",") != "1,2,3" {
		t.Fatalf("rows = %v", gotRows)
	}
	if final.DurationMS != 7 {
		t.Fatalf("terminal duration = %d, want 7", final.DurationMS)
	}

	// An error mid-stream surfaces on the terminal frame.
	raw, _ = json.Marshal(QueryStreamParams{ConnID: "c1", SQL: "boom", Batch: 2})
	req, _ = json.Marshal(Request{ID: 43, Method: MethodQueryStream, Params: raw})
	if _, err := c.w.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	sawErr := false
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			t.Fatalf("read err frame: %v", err)
		}
		var resp Response
		_ = json.Unmarshal([]byte(line), &resp)
		if resp.Done {
			sawErr = strings.Contains(resp.Error, "stream blew up")
			break
		}
	}
	if !sawErr {
		t.Fatal("expected the mid-stream error on the terminal frame")
	}
}
