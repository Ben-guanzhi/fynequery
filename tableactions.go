package main

import (
	"context"
	"fmt"
	"strings"
)

// Ported from the parent TableProGo project's tableactions.go: what the
// navigator's object menu can do to a table or a view. Each one is rendered
// here from the catalog's own name parts: the menu picks an action and a name,
// it cannot send a statement of its own.
const (
	actionDrop     = "drop"
	actionTruncate = "truncate"
	actionRename   = "rename"
)

// objectNames qualifies both sides of an action: the object as it is now, and -
// for a rename - as it will be.
type objectNames struct {
	dbType     string
	objectType string
	database   string
	schema     string
	table      string
	newName    string
}

func (o objectNames) current() (string, error) {
	return tableName(o.dbType, o.database, o.schema, o.table)
}

func (o objectNames) next() (string, error) {
	return tableName(o.dbType, o.database, o.schema, o.newName)
}

// sqlstring renders a name for a place SQL expects text rather than an
// identifier, which is what sp_rename does.
func sqlstring(dbType, value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	if dbType == dbTypeSQLServer {
		return "N'" + escaped + "'"
	}
	return "'" + escaped + "'"
}

// tableActionStatement renders one action against one object. schema is already
// the default filled in, and objectType is "table" or "view" as the catalog
// describes it - which is why the caller verifies the name before asking.
func tableActionStatement(o objectNames, action string) (string, error) {
	current, err := o.current()
	if err != nil {
		return "", err
	}
	view := o.objectType == "view"

	switch action {
	case actionDrop:
		if view {
			return "DROP VIEW " + current, nil
		}
		return "DROP TABLE " + current, nil

	case actionTruncate:
		if view {
			return "", fmt.Errorf("视图没有数据可清空")
		}
		switch o.dbType {
		case dbTypeMySQL, dbTypePostgres, dbTypeSQLServer, dbTypeOracle:
			return "TRUNCATE TABLE " + current, nil
		case dbTypeSQLite:
			// SQLite has no TRUNCATE; deleting every row is what its own
			// CLI does, and it keeps the sqlite_sequence counter alone.
			return "DELETE FROM " + current, nil
		}
		return "", fmt.Errorf("unsupported type: %s", o.dbType)

	case actionRename:
		if !isIdentifierLike(o.newName) {
			return "", fmt.Errorf("%q is not a valid new name", o.newName)
		}
		if o.newName == o.table {
			return "", fmt.Errorf("新名字和原来一样")
		}
		quotedNew, err := quoteIdent(o.dbType, o.newName)
		if err != nil {
			return "", err
		}
		switch o.dbType {
		case dbTypeMySQL:
			// MySQL's rename names both sides in full, so the table cannot
			// drift into another database on the way.
			next, err := o.next()
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("RENAME TABLE %s TO %s", current, next), nil
		case dbTypeSQLServer:
			// sp_rename takes text, not identifiers: brackets in the string
			// mean an escaped name rather than a quoted one.
			return fmt.Sprintf("EXEC sp_rename %s, %s",
				sqlstring(o.dbType, defaultSchema(o.dbType, o.schema)+"."+o.table),
				sqlstring(o.dbType, o.newName)), nil
		case dbTypePostgres, dbTypeOracle, dbTypeSQLite:
			return fmt.Sprintf("ALTER TABLE %s RENAME TO %s", current, quotedNew), nil
		}
		return "", fmt.Errorf("unsupported type: %s", o.dbType)
	}
	return "", fmt.Errorf("unknown table action %q", action)
}

// runTableAction validates the object against the catalog, renders the
// statement and executes it. It goes through the same execute path as a typed
// statement, so the action lands in the history with its error if it fails.
func (c *Client) runTableAction(connID, database, schema, table, action, newName string) (string, error) {
	conn, err := c.openConn(connID)
	if err != nil {
		return "", err
	}
	if database == "" {
		database = conn.Config.Database
	}
	schema = defaultSchema(conn.Config.Type, schema)

	tables, err := c.tables(connID, database, schema)
	if err != nil {
		return "", err
	}
	objectType := ""
	taken := map[string]bool{}
	for _, info := range tables {
		taken[strings.ToLower(info.Name)] = true
		if info.Name == table {
			objectType = info.Type
		}
	}
	if objectType == "" {
		return "", fmt.Errorf("%s 不在这个库里，也许已经被改掉，刷新侧栏再看一次", table)
	}
	newName = strings.TrimSpace(newName)
	if action == actionRename && taken[strings.ToLower(newName)] {
		return "", fmt.Errorf("%s 已经存在", newName)
	}

	statement, err := tableActionStatement(objectNames{
		dbType:     conn.Config.Type,
		objectType: objectType,
		database:   database,
		schema:     schema,
		table:      table,
		newName:    newName,
	}, action)
	if err != nil {
		return "", err
	}
	result := c.execute(context.Background(), conn, database, statement, 0)
	c.recordStatement(statement, result)
	if result.Error != "" {
		return statement, fmt.Errorf("%s", result.Error)
	}
	return statement, nil
}
