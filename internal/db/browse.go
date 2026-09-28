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
	filter := conn.Config.DatabaseFilter
	out := make([]string, 0, len(names))
	for _, name := range names {
		if filter.allows(name) {
			out = append(out, name)
		}
	}
	return out, nil
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
