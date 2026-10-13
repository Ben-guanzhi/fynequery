package main

import (
	"fmt"

	"fyne.io/fyne/v2"

	"github.com/Ben-guanzhi/TableProGo/tableproxlsx"

	"tablepro-fyne/internal/db"
)

// Thin Client-side wrappers around the db.Store barrier: connection lifecycle
// keeps the UI's notion of "current connection" in sync, and the foreign-key
// memo stays on this side because it is a UI-session cache.

// connect opens a connection and makes it the current one. Call it on the UI
// thread: the currentID write is under the session lock because a query
// snapshot takes it there, but the async connect path in ui_connections.go
// goes through store.Connect directly and sets the session in the callback fyne
// dispatches back to the main goroutine.
func (c *Client) connect(cfg db.ConnConfig) error {
	if err := c.store.Connect(cfg); err != nil {
		return err
	}
	c.mu.Lock()
	c.currentID = cfg.ID
	c.mu.Unlock()
	return nil
}

// disconnect closes a connection and drops it as current if it was.
func (c *Client) disconnect(id string) {
	c.store.Disconnect(id)
	c.mu.Lock()
	if c.currentID == id {
		c.currentID = ""
	}
	c.mu.Unlock()
}

// foreignKeys lists the constraints a table has, memoized per table for the
// session: the cell dialog opens on every edit and the catalog rarely changes.
func (c *Client) foreignKeys(connID, database, schema, table string) ([]db.ForeignKeyInfo, error) {
	conn, err := c.store.OpenConn(connID)
	if err != nil {
		return nil, err
	}
	if schema == "" {
		schema = db.ResolveSchema(conn.Config.Type, database, schema)
	}
	key := connID + "\x00" + database + "\x00" + schema + "\x00" + table
	c.mu.Lock()
	cached, ok := c.cache.fks[key]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}
	fks, err := c.store.TableForeignKeys(conn, database, schema, table)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache.fks[key] = fks
	c.mu.Unlock()
	return fks, nil
}

// foreignKeyValues answers "what could this cell hold", reusing the memoized
// constraint list so the picker does not re-read the catalog every time.
func (c *Client) foreignKeyValues(connID, database, schema, table, column string, limit int) (db.ForeignKeyCandidates, error) {
	conn, err := c.store.OpenConn(connID)
	if err != nil {
		return db.ForeignKeyCandidates{}, err
	}
	if schema == "" {
		schema = db.ResolveSchema(conn.Config.Type, database, schema)
	}
	fks, err := c.foreignKeys(connID, database, schema, table)
	if err != nil {
		return db.ForeignKeyCandidates{}, err
	}
	return c.store.ForeignKeyValues(connID, database, schema, table, column, fks, limit)
}

// writeWorkbookFile renders the current raw result as an xlsx file. Unlike
// the INSERT export this works for any result - a view or a custom query has
// cells too. The rendering lives in the shared tableproxlsx package; only the
// fyne file target belongs to this side of the barrier.
func (c *Client) writeWorkbookFile(f fyne.URIWriteCloser) error {
	if len(c.grid.header) == 0 {
		return fmt.Errorf("当前结果没有列，无法导出 XLSX")
	}
	if len(c.grid.lastRows) == 0 {
		return fmt.Errorf("当前结果为空，没有可导出的行")
	}
	columns := make([]tableproxlsx.Column, len(c.grid.header))
	for i, h := range c.grid.header {
		typ := ""
		if i < len(c.grid.colTypes) {
			typ = c.grid.colTypes[i]
		}
		columns[i] = tableproxlsx.Column{Name: db.StripTypeSuffix(h), Type: typ}
	}
	data, err := tableproxlsx.BuildWorkbook(columns, c.grid.lastRows)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}
