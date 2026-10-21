package drivercore

import (
	"encoding/json"
	"fmt"
	"time"
)

// A database cell travels over the wire as a tagged value, because JSON on its
// own cannot tell an int64 from a float64 (both arrive as a number) and cannot
// carry []byte or time.Time. The plugin scans its *sql.Rows into Go values,
// converts the display-shaped ones ([]byte -> string, time.Time -> RFC3339)
// exactly as the in-process path does, and tags each cell; the client rebuilds
// the Go value and funnels it through convertAssign, so a plugin result scans
// identically to an in-process one.
type cell struct {
	T string      `json:"t"`
	V interface{} `json:"v,omitempty"`
}

const (
	tagNil   = "n"
	tagInt   = "i"
	tagFloat = "f"
	tagBool  = "b"
	tagStr   = "s"
)

func encodeValue(v interface{}) cell {
	switch x := v.(type) {
	case nil:
		return cell{T: tagNil}
	case int64:
		return cell{T: tagInt, V: x}
	case int:
		return cell{T: tagInt, V: int64(x)}
	case float64:
		return cell{T: tagFloat, V: x}
	case bool:
		return cell{T: tagBool, V: x}
	case string:
		return cell{T: tagStr, V: x}
	case []byte:
		return cell{T: tagStr, V: string(x)}
	case time.Time:
		return cell{T: tagStr, V: x.Format(time.RFC3339)}
	default:
		return cell{T: tagStr, V: fmt.Sprintf("%v", x)}
	}
}

func decodeValue(c cell) interface{} {
	switch c.T {
	case tagNil:
		return nil
	case tagInt:
		switch n := c.V.(type) {
		case float64: // the JSON path: numbers arrive as float64
			return int64(n)
		case int64: // the in-memory path (no JSON round-trip)
			return n
		}
		return int64(0)
	case tagFloat:
		switch n := c.V.(type) {
		case float64:
			return n
		case int64:
			return float64(n)
		}
		return float64(0)
	case tagBool:
		b, _ := c.V.(bool)
		return b
	case tagStr:
		s, _ := c.V.(string)
		return s
	}
	return nil
}

func encodeRow(row []interface{}) []cell {
	out := make([]cell, len(row))
	for i, v := range row {
		out[i] = encodeValue(v)
	}
	return out
}

func decodeRow(cs []cell) []interface{} {
	out := make([]interface{}, len(cs))
	for i, c := range cs {
		out[i] = decodeValue(c)
	}
	return out
}

// queryChunk is one frame of a streamed resultset. The first frame carries the
// columns/types (the head); the rest carry batches of rows; the last frame sets
// Done. Err on any frame ends the stream with that error. Streaming in chunks
// keeps a large result from ever existing as one giant JSON frame on the wire -
// the plugin frees each batch as it sends it.
type queryChunk struct {
	Columns []string `json:"columns,omitempty"`
	Types   []string `json:"types,omitempty"`
	Rows    [][]cell `json:"rows,omitempty"`
	Done    bool     `json:"done,omitempty"`
	Err     string   `json:"err,omitempty"`
}

// queryChunkRows is how many rows travel in one frame.
const queryChunkRows = 500

// execReply is the wire form of a non-query statement.
type execReply struct {
	Affected     int64  `json:"affected"`
	LastInsertID int64  `json:"lastInsertId"`
	Err          string `json:"err,omitempty"`
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// The only values marshaled here are plain structs/strings the app
		// built, so a marshal failure is a programming error, not a runtime one.
		panic(fmt.Sprintf("drivercore: marshal %T: %v", v, err))
	}
	return b
}
