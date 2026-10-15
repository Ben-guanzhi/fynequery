package db

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Paging: re-run one statement with a row window and show the next slice. The
// window itself is rendered by the engine's dialect (dialect.pageSQL), so
// adding an engine stays a one-row change rather than an edit of this file.
//
// A page is only safe when the statement declares no row window of its own:
// appending LIMIT/OFFSET to SQL that already has one would either be a syntax
// error or silently override what the user wrote. ownLimitRe already spots
// those words for withRowLimit, so the same guard decides pageability here.

// MaxPageSize bounds one window so a mistyped page size cannot pull a whole
// table across the wire.
const MaxPageSize = 5000

// outerOrderByRe matches an ORDER BY that belongs to the statement itself, i.e.
// one with no closing paren after it. A derived table's ORDER BY sits inside
// parentheses and must not be mistaken for the outer one, which is why the
// pattern is anchored rather than just searching for the words.
var outerOrderByRe = regexp.MustCompile(`(?i)\border\s+by\b[^()]*$`)

// trimStatement drops surrounding whitespace and a trailing semicolon: the form
// every window renderer expects.
func trimStatement(query string) string {
	return strings.TrimRight(strings.TrimSpace(query), ";")
}

// Pageable reports whether one statement can be re-run as a page window. Only a
// SELECT/WITH statement that declares no row window of its own qualifies:
// SHOW/DESCRIBE/EXPLAIN return catalog output, and DML has no rows to page.
func Pageable(dbType, query string) bool {
	d, err := dialectFor(dbType)
	if err != nil || d.pageSQL == nil {
		return false
	}
	trimmed := trimStatement(query)
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return false
	}
	return !ownLimitRe.MatchString(trimmed)
}

// PageSQL renders query as its pageNo-th window (1-based) of size rows. It
// refuses anything but a pageable statement rather than emitting SQL the engine
// would reject or that would contradict the user's own window.
func PageSQL(dbType, query string, pageNo, size int) (string, error) {
	if !Pageable(dbType, query) {
		return "", fmt.Errorf("statement cannot be paged: only a SELECT without its own row window qualifies")
	}
	if pageNo < 1 {
		return "", fmt.Errorf("page number starts at 1, got %d", pageNo)
	}
	if size < 1 || size > MaxPageSize {
		return "", fmt.Errorf("page size must be within 1..%d, got %d", MaxPageSize, size)
	}
	d, err := dialectFor(dbType)
	if err != nil {
		return "", err
	}
	return d.pageSQL(trimStatement(query), (pageNo-1)*size, size), nil
}

// PageNeedsStableOrder reports whether one statement leaves its row order to
// the engine. Without an ORDER BY of its own there is no promise about which
// rows a window picks, so consecutive pages can show one row twice or skip it -
// the reason the reference client's pageOrder injects a primary-key sort. Only
// the statement's own ORDER BY counts: a derived table's ORDER BY is inside
// parentheses and does not order the rows the window slices.
func PageNeedsStableOrder(query string) bool {
	return !outerOrderByRe.MatchString(trimStatement(query))
}

// WithOrderBy appends an ORDER BY over cols, each quoted for the engine. This
// is how a pager makes the windows of one statement disjoint.
func WithOrderBy(dbType, query string, cols []string) (string, error) {
	if len(cols) == 0 {
		return "", fmt.Errorf("no ordering columns given")
	}
	terms := make([]string, 0, len(cols))
	for _, col := range cols {
		q, err := quoteIdent(dbType, col)
		if err != nil {
			return "", err
		}
		terms = append(terms, q)
	}
	return trimStatement(query) + " ORDER BY " + strings.Join(terms, ", "), nil
}

// limitOffset renders the window clause MySQL, PostgreSQL and SQLite take.
func limitOffset(query string, offset, size int) string {
	return fmt.Sprintf("%s LIMIT %d OFFSET %d", query, size, offset)
}

// offsetFetch renders the ANSI window clause SQL Server and Oracle take.
func offsetFetch(query string, offset, size int) string {
	return fmt.Sprintf("%s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", query, offset, size)
}

// ExecutePage runs a pageable statement again, restricted to one row window.
// Unlike Execute it never applies the 行数上限 cap - the window is the cap, and
// stacking both would silently shorten a page. It records no history entry
// either: turning to another page is not a new statement.
func (c *Store) ExecutePage(ctx context.Context, conn *Conn, database, query string, pageNo, size int) QueryResult {
	page, err := PageSQL(conn.Config.Type, query, pageNo, size)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	pool, err := c.queryPool(ctx, conn, database)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	return runSelect(ctx, pool, page, time.Now())
}
