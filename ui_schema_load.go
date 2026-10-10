package main

// Schema tree loading: fetching catalog data from the store, mutating the
// node table (c.tree.nodes), and the taps that drive the editor from a node.
// Rendering of those nodes lives in ui_schema.go; node action dialogs in
// ui_schema_actions.go.

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"

	"tablepro-fyne/internal/db"
)

// onSchemaTableTapped fills the editor with a SELECT preview for the table.
func (c *Client) onSchemaTableTapped(uid string) {
	nd, ok := c.tree.nodes[uid]
	if !ok || nd.kind != "table" {
		return
	}
	c.tree.selected = uid
	c.currentID = c.selectedOrLive()
	c.currentDB = nd.db
	c.tabs.entry.setTextSilently(c.selectSQL(nd))
	c.dbSelRefresh()
	c.rebuildSchemaRows()
	c.focusEditor() // the next keystrokes should edit the generated SELECT
}

// selectedOrLive returns a usable connection id, preferring user selection.
func (c *Client) selectedOrLive() string {
	if c.currentID != "" {
		return c.currentID
	}
	if cfg, ok := c.selectedConn(); ok {
		return cfg.ID
	}
	return ""
}

// selectSQL builds a preview SELECT for a table node.
func (c *Client) selectSQL(n *nodeData) string {
	conn, err := c.store.OpenConn(c.currentID)
	if err != nil {
		return "SELECT * FROM " + plainIdent(n.name)
	}
	typ := conn.Config.Type

	schema := n.schema
	if schema == "" {
		schema = db.ResolveSchema(typ, n.db, schema)
	}
	var parts []string
	switch typ {
	case db.DbTypeMySQL:
		parts = []string{n.db, n.name}
	case db.DbTypePostgres, db.DbTypeSQLServer:
		parts = []string{schema, n.name}
	default:
		parts = []string{n.name}
	}
	q, e := db.QualifiedName(typ, parts...)
	if e != nil {
		return "SELECT * FROM " + plainIdent(n.name)
	}
	return "SELECT * FROM " + q
}

// plainIdent returns an unquoted identifier fallback for SELECT previews.
func plainIdent(name string) string {
	if strings.ContainsAny(name, " \t\"") {
		return fmt.Sprintf("[%s]", name)
	}
	return name
}

// refreshSchema reloads the top level (databases); deeper levels load on
// demand when their row is tapped.
func (c *Client) refreshSchema() {
	c.mu.Lock()
	c.tree.nodes = map[string]*nodeData{treeRootUID: {kind: "root", label: "数据库", lower: "数据库"}}
	c.mu.Unlock()
	c.tree.expanded = make(map[string]bool)
	c.tree.selected = ""
	c.tree.cache = make(map[rowCacheKey]fyne.CanvasObject) // the old tree is gone; drop its rendered rows

	id := c.selectedOrLive()
	if id == "" || !c.store.ConnectionStatus(id) {
		c.mu.Lock()
		root := c.tree.nodes[treeRootUID]
		root.label = "未连接"
		root.lower = "未连接"
		c.mu.Unlock()
		c.rebuildSchemaRows()
		return
	}

	c.setStatus("加载数据库 ...")
	dbs, err := c.store.Databases(id)
	if err != nil {
		c.mu.Lock()
		root := c.tree.nodes[treeRootUID]
		root.label = "加载失败：" + err.Error()
		root.lower = strings.ToLower(root.label)
		c.mu.Unlock()
		c.rebuildSchemaRows()
		return
	}

	c.mu.Lock()
	root := c.tree.nodes[treeRootUID]
	for _, dbName := range dbs {
		dbUID := "db:" + dbName
		c.tree.nodes[dbUID] = &nodeData{kind: "db", label: dbName, lower: strings.ToLower(dbName), db: dbName, uid: dbUID}
		root.children = append(root.children, dbUID)
	}
	root.label = fmt.Sprintf("数据库 (%d)", len(dbs))
	root.lower = strings.ToLower(root.label)
	c.mu.Unlock()

	// Open the connection's own database right away, all the way down to its
	// tables — the reference navigator shows them the moment you connect.
	if cfg, ok := c.store.LiveConfig(id); ok && cfg.Database != "" {
		if uid := "db:" + cfg.Database; c.tree.nodes[uid] != nil {
			c.tree.expanded[uid] = true
			dbNode := c.tree.nodes[uid]
			c.loadChildren(dbNode)
			for _, ch := range dbNode.children {
				if s := c.tree.nodes[ch]; s != nil && s.kind == "schema" {
					c.tree.expanded[s.uid] = true
					c.loadChildren(s) // tables of this schema
				}
			}
		}
	}

	c.rebuildSchemaRows()
	c.setStatus("结构已加载")
}

// loadChildren loads a node's children from the server. The catalog query
// runs off the UI thread; the tree mutation and re-render come back on the
// main goroutine, so expanding the navigator never freezes the window.
func (c *Client) loadChildren(nd *nodeData) {
	if nd.loaded {
		return
	}
	nd.loaded = true
	nd.loading = true
	id := c.selectedOrLive()
	if id == "" {
		nd.loading = false
		return
	}
	fetch := func() (schemas []string, tables []db.TableInfo, err error) {
		switch nd.kind {
		case "db":
			schemas, err = c.store.Schemas(id, nd.db)
			if err == nil && len(schemas) == 0 {
				tables, err = c.store.Tables(id, nd.db, "")
			}
		case "schema":
			tables, err = c.store.Tables(id, nd.db, nd.schema)
		}
		return
	}
	if c.asyncRun {
		go func() {
			schemas, tables, err := fetch()
			c.dispatch(func() { c.applyChildren(nd, schemas, tables, err) })
		}()
		return
	}
	schemas, tables, err := fetch()
	c.applyChildren(nd, schemas, tables, err)
}

// applyChildren attaches fetched children to a node and re-renders the
// navigator. UI-thread only.
func (c *Client) applyChildren(nd *nodeData, schemas []string, tables []db.TableInfo, err error) {
	nd.loading = false
	switch {
	case err != nil:
		c.addErrorNode(nd, err)
	case len(schemas) > 0:
		c.attachSchemas(nd, schemas)
		// An expanded database also loads its schemas' tables so the tree
		// fills in like the reference navigator.
		for _, ch := range nd.children {
			if s := c.tree.nodes[ch]; s != nil && s.kind == "schema" {
				c.tree.expanded[s.uid] = true
				c.loadChildren(s)
			}
		}
	default:
		c.attachTables(nd, tables)
	}
	if c.tree.expanded[nd.uid] {
		c.rebuildSchemaRows()
	}
}

// attachSchemas turns fetched schema names into child nodes.
func (c *Client) attachSchemas(nd *nodeData, schemas []string) {
	for _, sch := range schemas {
		schUID := nd.uid + "|s:" + sch
		c.mu.Lock()
		c.tree.nodes[schUID] = &nodeData{kind: "schema", label: sch, lower: strings.ToLower(sch), db: nd.db, schema: sch, uid: schUID}
		nd.children = append(nd.children, schUID)
		c.mu.Unlock()
	}
}

// attachTables turns fetched table rows into child nodes; a result with no
// rows gets a diagnostic node instead of a silent blank expansion.
func (c *Client) attachTables(nd *nodeData, tables []db.TableInfo) {
	if len(tables) == 0 {
		uid := nd.uid + "|empty"
		c.mu.Lock()
		c.tree.nodes[uid] = &nodeData{
			kind:   "empty",
			label:  "（没有可见的表：登录可能无权限，或该库为空）",
			lower:  "（没有可见的表：登录可能无权限，或该库为空）",
			uid:    uid,
			parent: nd.uid,
		}
		nd.children = []string{uid}
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range tables {
		tSchema := t.Schema
		if tSchema == "" {
			tSchema = nd.schema
		}
		label := fmt.Sprintf("%s [%s]", t.Name, t.Type)
		if tSchema != nd.schema {
			// The row came from the cross-schema fallback: show where it lives.
			label = fmt.Sprintf("%s.%s [%s]", tSchema, t.Name, t.Type)
		}
		tUID := nd.uid + "|t:" + tSchema + "." + t.Name
		c.tree.nodes[tUID] = &nodeData{
			kind:   "table",
			label:  label,
			lower:  strings.ToLower(label),
			db:     nd.db,
			schema: tSchema,
			name:   t.Name,
			uid:    tUID,
			typ:    t.Type,
		}
		nd.children = append(nd.children, tUID)
	}
}

// addErrorNode attaches a red, retryable error row under nd, replacing any
// children (a failed load produces none anyway).
func (c *Client) addErrorNode(nd *nodeData, err error) {
	uid := nd.uid + "|error"
	label := "⚠ 加载失败：" + err.Error()
	c.mu.Lock()
	c.tree.nodes[uid] = &nodeData{
		kind:   "error",
		label:  label,
		lower:  strings.ToLower(label),
		uid:    uid,
		parent: nd.uid,
	}
	nd.children = []string{uid}
	c.mu.Unlock()
	c.setStatus("加载失败：" + err.Error())
}

// retrySchemaNode clears a failed load and expands the parent again.
func (c *Client) retrySchemaNode(parentUID string) {
	parent := c.tree.nodes[parentUID]
	if parent == nil {
		return
	}
	parent.loaded = false
	parent.children = nil
	delete(c.tree.expanded, parentUID)
	c.toggleSchemaNode(parentUID)
}
