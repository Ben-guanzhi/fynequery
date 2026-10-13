package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Ben-guanzhi/TableProGo/tableprosql"
)

func isSelectQuery(query string) bool {
	q := strings.ToUpper(strings.TrimSpace(query))
	return strings.HasPrefix(q, "SELECT") ||
		strings.HasPrefix(q, "WITH") || // CTE
		strings.HasPrefix(q, "SHOW") ||
		strings.HasPrefix(q, "DESCRIBE") ||
		strings.HasPrefix(q, "EXPLAIN")
}

// ownLimitRe spots SQL that already caps its own rows; paging's pageability
// guard uses the same notion of "capped" as the shared RowLimit.
var ownLimitRe = regexp.MustCompile(`(?i)\b(top|limit|offset|fetch|rownum)\b`)

// withRowLimit caps a plain SELECT (or a WITH ... SELECT CTE) so grids stay
// responsive; the shapes live in the shared SQL text package.
func withRowLimit(query, dbType string, limit int) string {
	return tableprosql.RowLimit(dbType, query, limit)
}

// runSelect scans a resultset into a QueryResult. A cancelled context aborts
// the query and is reported through the result's error.
func runSelect(ctx context.Context, db *sql.DB, query string, start time.Time) QueryResult {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	names := make([]string, len(cols))
	types := make([]string, len(cols))
	for i, name := range cols {
		names[i] = name
		dt := ""
		if colTypes[i] != nil {
			dt = colTypes[i].DatabaseTypeName()
		}
		types[i] = dt
	}

	values := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	out := make([][]interface{}, 0, 64)
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return QueryResult{Error: err.Error()}
		}
		row := make([]interface{}, len(values))
		for i, v := range values {
			switch val := v.(type) {
			case []byte:
				row[i] = string(val)
			case time.Time:
				row[i] = val.Format(time.RFC3339)
			default:
				row[i] = val
			}
		}
		out = append(out, row)
	}
	// A resultset cut short by a network or driver error must not pass as a
	// complete result.
	if err := rows.Err(); err != nil {
		return QueryResult{Error: err.Error()}
	}
	return QueryResult{
		Columns:       names,
		ColTypes:      types,
		Rows:          out,
		RowCount:      len(out),
		ExecutionTime: time.Since(start).Seconds(),
	}
}

// executeCommand runs a non-SELECT statement.
func executeCommand(ctx context.Context, db *sql.DB, query string, start time.Time) QueryResult {
	res, err := db.ExecContext(ctx, query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	affected, _ := res.RowsAffected()
	lastID, _ := res.LastInsertId()
	return QueryResult{
		AffectedRows:  affected,
		LastInsertID:  lastID,
		ExecutionTime: time.Since(start).Seconds(),
	}
}

// execute dispatches a single statement against a connection. The context
// carries a cancellation; cancelling it aborts a running query.
func (c *Store) Execute(ctx context.Context, conn *Conn, database, query string, limit int) QueryResult {
	db, err := c.queryPool(conn, database)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	if limit > 0 {
		query = withRowLimit(query, conn.Config.Type, limit)
	}
	start := time.Now()
	if isSelectQuery(query) {
		return runSelect(ctx, db, query, start)
	}
	return executeCommand(ctx, db, query, start)
}

// runScript executes statements in order, stopping at the first failure. It
// returns the last result together with the statement that produced it (the
// failed statement itself when okCount < len(stmts)), so the grid shows where
// the script ended and the caller can say which statement failed.
func (c *Store) RunScript(ctx context.Context, conn *Conn, database string, stmts []string, limit int) (last QueryResult, lastStmt string, okCount int) {
	for _, stmt := range stmts {
		if ctx.Err() != nil {
			return QueryResult{Error: ctx.Err().Error()}, stmt, okCount
		}
		res := c.Execute(ctx, conn, database, stmt, limit)
		c.RecordStatement(conn.Config.ID, database, stmt, res)
		if res.Error != "" {
			return res, stmt, okCount
		}
		last = res
		lastStmt = stmt
		okCount++
	}
	return last, lastStmt, okCount
}

// openConn returns an open connection by id, or a readable error.
func (c *Store) OpenConn(id string) (*Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, ok := c.live[id]
	if !ok || conn.DB == nil {
		return nil, fmt.Errorf("not connected")
	}
	return conn, nil
}

// quoteIdent safely quotes an identifier for each engine; the shapes live in
// the shared SQL text package.
func quoteIdent(dbType, name string) (string, error) {
	return tableprosql.QuoteIdent(dbType, name)
}

func QualifiedName(dbType string, parts ...string) (string, error) {
	return tableprosql.QualifiedName(dbType, parts...)
}

func sqlLiteral(value string) string {
	return "N'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// ResolveSchema names the namespace catalog queries run against inside one
// database; the per-engine answers live on the dialect (dialect.go).
func ResolveSchema(dbType, database, schema string) string {
	d, err := dialectFor(dbType)
	if err != nil {
		return schema
	}
	return d.catalogSchema(database, schema)
}

func normalizeTableType(raw string) string {
	if strings.Contains(strings.ToUpper(raw), "VIEW") {
		return "view"
	}
	return "table"
}

// ValueString renders one cell value for the grid.
func ValueString(v interface{}) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprintf("%v", v)
}
