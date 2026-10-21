package pluginkit

import (
	"database/sql"
	"testing"

	"fynequery/proto"
)

type nilMapDialect struct{}

func (nilMapDialect) Code() string        { return "test" }
func (nilMapDialect) Name() string        { return "Test" }
func (nilMapDialect) Version() string     { return "0" }
func (nilMapDialect) DefaultPort() string { return "0" }

func (nilMapDialect) Open(proto.ConnectParams) (*sql.DB, string, error)       { return nil, "", nil }
func (nilMapDialect) ListTables(*sql.DB) ([]proto.ObjectInfo, error)          { return nil, nil }
func (nilMapDialect) ListColumns(*sql.DB, string) ([]proto.ColumnInfo, error) { return nil, nil }
func (nilMapDialect) BrowseSQL(string, int) string                            { return "" }

// TestNewHandlerInitializesMaps guards against the crash where Open wrote to
// handler.server/conns before they were allocated ("assignment to entry in nil
// map"), which killed the plugin process on the first successful connection.
func TestNewHandlerInitializesMaps(t *testing.T) {
	h := newHandler(nilMapDialect{})
	if h.conns == nil {
		t.Fatal("conns map is nil")
	}
	if h.server == nil {
		t.Fatal("server map is nil")
	}
	// Writing must not panic.
	h.mu.Lock()
	h.conns["c1"] = nil
	h.server["c1"] = "x"
	h.mu.Unlock()
}
