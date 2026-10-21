package db

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"tablepro-fyne/internal/drivercore"
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

// RowCapStatus is what the row cap does to one statement: bind, or decline and
// leave the SQL exactly as written.
type RowCapStatus int

const (
	// RowCapNotRequested means no cap was asked for ("无限制"), so nothing was
	// injected and the statement can return any number of rows.
	RowCapNotRequested RowCapStatus = iota
	// RowCapApplied means the cap was injected, so the grid cannot exceed it.
	RowCapApplied
	// RowCapOwnWindow means the statement already caps its own rows.
	RowCapOwnWindow
	// RowCapShapeUnsafe means the statement reads as a query but the renderer
	// refuses to inject into its shape (SQL Server caps with TOP, which cannot
	// go into a WITH ... SELECT).
	RowCapShapeUnsafe
)

// JudgeRowCap answers, before a run, whether the chosen 行数上限 will reach this
// statement. The verdict comes from the shared renderer rather than a second
// copy of its guard: RowLimit hands back the untouched query exactly when it
// declines, so the two can never drift apart.
func JudgeRowCap(dbType, query string, limit int) RowCapStatus {
	if limit <= 0 {
		return RowCapNotRequested
	}
	if tableprosql.RowLimit(dbType, query, limit) != query {
		return RowCapApplied
	}
	if ownLimitRe.MatchString(trimStatement(query)) {
		return RowCapOwnWindow
	}
	return RowCapShapeUnsafe
}

// runSelect scans a resultset into a QueryResult. A cancelled context aborts
// the query and is reported through the result's error.
func runSelect(ctx context.Context, engine drivercore.Engine, query string, start time.Time) QueryResult {
	rows, err := engine.Query(ctx, query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	defer rows.Close()

	names := rows.Columns()
	types := rows.ColTypes()

	values := make([]interface{}, len(names))
	ptrs := make([]interface{}, len(names))
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
func executeCommand(ctx context.Context, engine drivercore.Engine, query string, start time.Time) QueryResult {
	res, err := engine.Exec(ctx, query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	return QueryResult{
		AffectedRows:  res.Affected,
		LastInsertID:  res.LastInsertID,
		ExecutionTime: time.Since(start).Seconds(),
	}
}

// Execute dispatches a single statement against a connection. The context
// carries a cancellation; cancelling it aborts a running query. A positive
// limit caps a SELECT's rows and, when the cap binds, says whether it cut them
// short.
func (c *Store) Execute(ctx context.Context, conn *Conn, database, query string, limit int) QueryResult {
	engine, err := c.queryPool(ctx, conn, database)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	start := time.Now()
	// isSelectQuery is answered on the text the user wrote, and so is the cap:
	// RowLimit only ever rewrites a SELECT or a WITH, so the two questions have
	// the same answer either way, and one text is what the probe is compared
	// against below.
	if isSelectQuery(query) {
		// Fetch one row past the cap. A grid full of exactly "limit" rows then
		// has two possible stories - the table holds exactly that many, or it
		// holds more and the rest never arrived - and the row count cannot tell
		// them apart, so the extra row decides and is dropped again.
		fetch := limit
		if limit > 0 {
			fetch = limit + 1
		}
		probe := withRowLimit(query, conn.Config.Type, fetch)
		result := runSelect(ctx, engine, probe, start)
		// The extra row is a probe only when the cap is ours: a statement with a
		// window of its own is run as written, and trimming its rows would be
		// the client silently editing an answer the server was told to shape.
		if limit > 0 && probe != query && result.Error == "" && len(result.Rows) > limit {
			result.Rows = result.Rows[:limit]
			result.RowCount = limit
			result.Truncated = true
		}
		return result
	}
	return executeCommand(ctx, engine, query, start)
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
	if !ok || !conn.isOpen() {
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
