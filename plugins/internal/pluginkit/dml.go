package pluginkit

// Centralized DML builders for the editable grid. They turn a primary-key map
// and a changes/values map into dialect-correct UPDATE/INSERT/DELETE SQL using
// the dialect's QuoteIdent and Placeholder, so every plugin engine shares one
// correct implementation. The host (db/mysql.go) uses an equivalent builder
// for the built-in driver; keeping the rules in one place avoids the two
// drifting apart.

import (
	"strconv"

	"context"
	"database/sql"
	"errors"
	"fynequery/internal/sqlkit"
	"fynequery/proto"
	"strings"
)

// errBusy signals a transient lock/busy condition that is worth retrying
// (SQLite under concurrent writers is the usual culprit).
var errBusy = errors.New("database is busy/locked")

// isDefault reports whether v carries the DEFAULT sentinel (proto.DefaultMarker).
func isDefault(v *string) bool { return v != nil && *v == proto.DefaultMarker }

// cellStrings renders tagged cells as the nullable strings the SQL builders take.
// A NULL stays nil, so "no value" and "the empty string" stay apart.
func cellStrings(cells map[string]proto.CellPayload) map[string]*string {
	out := make(map[string]*string, len(cells))
	for name, cell := range cells {
		if cell.Kind == "null" || cell.Kind == "" {
			out[name] = nil
			continue
		}
		text := cellText(cell)
		out[name] = &text
	}
	return out
}

// cellText is the text of one tagged cell.
func cellText(cell proto.CellPayload) string {
	switch cell.Kind {
	case "int":
		return strconv.FormatInt(cell.Int, 10)
	case "float":
		return strconv.FormatFloat(cell.Flt, 'g', -1, 64)
	case "text":
		return cell.Text
	case "bool":
		return strconv.FormatBool(cell.Bool)
	case "blob":
		return string(cell.Blob)
	case "time":
		return cell.Time
	default:
		return ""
	}
}

// argOf converts a *string cell into a driver argument: nil → NULL, the DEFAULT
// sentinel is handled by the callers (they emit the keyword instead of a
// placeholder), so here it falls through to its literal text.
func argOf(v *string) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// buildUpdate renders "UPDATE table SET c=? WHERE pk=?" using the dialect's
// quoting and placeholder style. A DEFAULT sentinel in changes emits the
// keyword DEFAULT rather than a bound parameter.
func buildUpdate(d Dialect, table string, pk, changes map[string]*string) (string, []interface{}) {
	var sets, where []string
	var args []interface{}
	i := 1
	for c, v := range changes {
		if isDefault(v) {
			sets = append(sets, d.QuoteIdent(c)+" = DEFAULT")
			continue
		}
		sets = append(sets, d.QuoteIdent(c)+" = "+d.Placeholder(i))
		args = append(args, argOf(v))
		i++
	}
	for c, v := range pk {
		where = append(where, d.QuoteIdent(c)+" = "+d.Placeholder(i))
		args = append(args, argOf(v))
		i++
	}
	q := "UPDATE " + d.QuoteIdent(table) + " SET " + strings.Join(sets, ", ") +
		" WHERE " + strings.Join(where, " AND ")
	return q, args
}

// buildDelete renders "DELETE FROM table WHERE pk=?".
func buildDelete(d Dialect, table string, pk map[string]*string) (string, []interface{}) {
	var where []string
	var args []interface{}
	i := 1
	for c, v := range pk {
		where = append(where, d.QuoteIdent(c)+" = "+d.Placeholder(i))
		args = append(args, argOf(v))
		i++
	}
	q := "DELETE FROM " + d.QuoteIdent(table) + " WHERE " + strings.Join(where, " AND ")
	return q, args
}

// buildInsert renders "INSERT INTO table (cols) VALUES (?)". Columns whose
// value is the DEFAULT sentinel are omitted entirely so the server applies its
// own default. Returns an error when there is nothing to insert.
func buildInsert(d Dialect, table string, values map[string]*string) (string, []interface{}, error) {
	var cols, ph []string
	var args []interface{}
	i := 1
	for c, v := range values {
		if isDefault(v) {
			continue // omit the column so the server applies its default
		}
		cols = append(cols, d.QuoteIdent(c))
		ph = append(ph, d.Placeholder(i))
		args = append(args, argOf(v))
		i++
	}
	if len(cols) == 0 {
		return "", nil, errors.New("insert: no values")
	}
	q := "INSERT INTO " + d.QuoteIdent(table) + " (" + strings.Join(cols, ", ") +
		") VALUES (" + strings.Join(ph, ", ") + ")"
	return q, args, nil
}

// runDML executes a single DML statement and reports affected rows. The
// querier is *sql.DB for autocommit or *sql.Tx when a manual transaction is
// active on the connection.
func runDML(q sqlkit.Querier, stmt string, args []interface{}) (proto.DMLResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sqlkit.Timeout)
	defer cancel()
	res, err := q.ExecContext(ctx, stmt, args...)
	if err != nil {
		return proto.DMLResult{}, err
	}
	n, _ := res.RowsAffected()
	return proto.DMLResult{RowsAffected: n}, nil
}

// txExecer is the subset of *sql.DB / *sql.Tx used by runBatch's transaction.
type txExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// batchStmt is one statement queued inside an ApplyBatch transaction.
type batchStmt struct {
	sql   string
	args  []interface{}
	isDML bool
}

// runBatchOpen opens a fresh transaction on db, runs every statement atomically
// and commits. Each DML statement must affect exactly one row; a mismatch
// rolls the whole batch back. DDL is exempt from the row-count check. A
// transient busy/locked condition is reported as errBusy for the caller to
// retry. Used when no manual transaction is active (ApplyChanges autocommits).
func runBatchOpen(db *sql.DB, stmts []batchStmt) (int64, error) {
	if len(stmts) == 0 {
		return 0, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, mapBusy(err)
	}
	defer tx.Rollback() // no-op once Commit succeeds
	total, err := runStmts(tx, stmts)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, mapBusy(err)
	}
	return total, nil
}

// runBatchTx runs every statement on an already-open transaction (a manual
// transaction started via Begin). It does NOT commit or roll back, leaving
// commit/rollback to the manual transaction's own lifecycle.
func runBatchTx(tx *sql.Tx, stmts []batchStmt) (int64, error) {
	return runStmts(tx, stmts)
}

// runStmts executes stmts on a transaction-like executor and returns the total
// affected-row count, enforcing the affect-exactly-one-row rule for DML.
func runStmts(x txExecer, stmts []batchStmt) (int64, error) {
	var total int64
	for _, s := range stmts {
		res, err := x.Exec(s.sql, s.args...)
		if err != nil {
			return 0, mapBusy(err)
		}
		if s.isDML {
			n, _ := res.RowsAffected()
			if n != 1 {
				return 0, ErrRowMismatch
			}
			total += n
		}
	}
	return total, nil
}

// ErrRowMismatch is returned when a DML statement did not affect exactly one
// row, indicating the target row was concurrently changed or no longer exists.
var ErrRowMismatch = errors.New("row mismatch: expected to affect exactly 1 row")

// mapBusy rewrites engine-specific lock/busy errors into errBusy so the caller
// can decide to retry. SQLite ("database is locked"/"busy") is the main case.
func mapBusy(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"database is locked", "sqlite_busy", "is busy", "lock wait timeout", "deadlock"} {
		if strings.Contains(msg, needle) {
			return errBusy
		}
	}
	return err
}
