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
