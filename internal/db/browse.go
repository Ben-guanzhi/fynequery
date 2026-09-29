package db

// Client-level catalog readers: apply-generic over the dialect table in
// dialect.go. The engine-specific SQL lives there; what stays here is the
// barrier code - connection handling, schema defaults and the database filter.

// databases lists databases for the live connection, applying the filter.
func (c *Store) Databases(id string) ([]string, error) {
	conn, err := c.OpenConn(id)
	if err != nil {
		return nil, err
	}
	if conn.Config.Type == DbTypeSQLite {
		// The single database is the file itself; a name filter must not be
		// able to hide it away.
		return []string{conn.Config.Database}, nil
	}
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		return nil, err
	}
	names, err := d.databases(c, conn)
	if err != nil {
		return nil, err
	}
	return c.filterNames(id, names), nil
}

// filterNames hides the databases the saved filter rejects.
//
// The filter comes back from the saved config rather than from the live
// connection: editing a connection rewrites the saved copy while the live Conn
// keeps the config it opened with (its fields are read without a lock, so nobody
// may change them underneath a running query). Reading the saved copy is what
// makes a filter edit land on the next refresh instead of the next reconnect -
// the same choice the parent app makes in savedDatabaseFilter.
func (c *Store) filterNames(id string, names []string) []string {
	cfg, ok := c.FindConnection(id)
	if !ok {
		out := make([]string, len(names))
		copy(out, names)
		return out
	}
	mask := cfg.DatabaseFilter
	out := make([]string, 0, len(names))
	for _, name := range names {
		if mask.allows(name) {
			out = append(out, name)
		}
	}
	return out
}

// schemas lists schemas for engines that have them (PostgreSQL, SQL Server).
func (c *Store) Schemas(id, database string) ([]string, error) {
	conn, err := c.OpenConn(id)
	if err != nil {
		return nil, err
	}
	if _, err := c.poolFor(conn, database); err != nil {
		return nil, err
	}
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		return nil, err
	}
	if d.schemas == nil {
		// MySQL treats the database as its schema; SQLite has a single main
		// schema; Oracle is introspected through the connected user.
		return nil, nil
	}
	return d.schemas(c, conn, database)
}

// tables lists tables and views for a database/schema.
func (c *Store) Tables(id, database, schema string) ([]TableInfo, error) {
	conn, err := c.OpenConn(id)
	if err != nil {
		return nil, err
	}
	if schema == "" {
		switch conn.Config.Type {
		case DbTypeSQLServer:
			schema = "dbo"
		case DbTypePostgres:
			schema = "public"
		}
	}
	d, err := dialectFor(conn.Config.Type)
	if err != nil {
		return nil, err
	}
	return d.tables(c, conn, database, schema)
}
