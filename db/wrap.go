package db

import (
	"fmt"
	"fynequery/proto"
)

// unwrapper is implemented by every Session decorator (tunnel, pool, …) so the
// capability helpers below can see through arbitrary nesting to the real
// session.
type unwrapper interface {
	innerSession() Session
}

// AsUpdater reports whether s can edit rows, unwrapping any decorators. Because
// a capabilityWrapper UNCONDITIONALLY satisfies Updater, a bare s.(Updater)
// assertion would wrongly report editing support for a wrapped connection whose
// inner session is not editable. These helpers recurse to the innermost real
// session before answering.
func AsUpdater(s Session) (Updater, bool) {
	if u, ok := s.(Updater); ok {
		if w, ok := s.(unwrapper); ok {
			if inner, ok := AsUpdater(w.innerSession()); ok {
				return inner, true
			}
			return nil, false
		}
		return u, true
	}
	return nil, false
}

// AsBatchUpdater reports whether s can apply a batch of changes.
func AsBatchUpdater(s Session) (BatchUpdater, bool) {
	if b, ok := s.(BatchUpdater); ok {
		if w, ok := s.(unwrapper); ok {
			if inner, ok := AsBatchUpdater(w.innerSession()); ok {
				return inner, true
			}
			return nil, false
		}
		return b, true
	}
	return nil, false
}

// AsCanceller reports whether s can cancel a running query.
func AsCanceller(s Session) (Canceller, bool) {
	if c, ok := s.(Canceller); ok {
		if w, ok := s.(unwrapper); ok {
			if inner, ok := AsCanceller(w.innerSession()); ok {
				return inner, true
			}
			return nil, false
		}
		return c, true
	}
	return nil, false
}

// AsDatabaseCreator reports whether s can create a database.
func AsDatabaseCreator(s Session) (DatabaseCreator, bool) {
	if c, ok := s.(DatabaseCreator); ok {
		if w, ok := s.(unwrapper); ok {
			if inner, ok := AsDatabaseCreator(w.innerSession()); ok {
				return inner, true
			}
			return nil, false
		}
		return c, true
	}
	return nil, false
}

// AsTransactional reports whether s supports a manual transaction.
func AsTransactional(s Session) (Transactional, bool) {
	if t, ok := s.(Transactional); ok {
		if w, ok := s.(unwrapper); ok {
			if inner, ok := AsTransactional(w.innerSession()); ok {
				return inner, true
			}
			return nil, false
		}
		return t, true
	}
	return nil, false
}

// AsStreamer reports whether s can stream query results incrementally.
func AsStreamer(s Session) (Streamer, bool) {
	if st, ok := s.(Streamer); ok {
		if w, ok := s.(unwrapper); ok {
			if inner, ok := AsStreamer(w.innerSession()); ok {
				return inner, true
			}
			return nil, false
		}
		return st, true
	}
	return nil, false
}

// AsMetricser reports whether s can report server metrics.
func AsMetricser(s Session) (Metricser, bool) {
	if m, ok := s.(Metricser); ok {
		if w, ok := s.(unwrapper); ok {
			if inner, ok := AsMetricser(w.innerSession()); ok {
				return inner, true
			}
			return nil, false
		}
		return m, true
	}
	return nil, false
}

// capabilityWrapper embeds a Session and forwards the OPTIONAL capability
// interfaces (Updater, BatchUpdater, Canceller, DatabaseCreator, and any added
// later) to the inner session via type assertions.
//
// Embedding a Session value only promotes the base Session methods; the extra
// capability methods live on the concrete inner type, so they are NOT promoted.
// Without explicit forwarding, wrapping a session (SSH tunnel, connection pool)
// would silently drop editing / cancel / create for the wrapped connection.
//
// NOTE: because these forwarding methods are always declared, a wrapper
// UNCONDITIONALLY satisfies the capability interfaces. Capability detection
// must therefore go through AsUpdater / AsCanceller / etc. (which unwrap the
// wrapper to the real inner session) rather than a bare type assertion, so an
// un-editable wrapped connection is still correctly reported as such.
type capabilityWrapper struct {
	Session
}

// innerSession returns the wrapped session so capability detection can see
// through decorators (tunnel → pool → …) to the real session.
func (w *capabilityWrapper) innerSession() Session { return w.Session }

func wrapCapabilities(inner Session) Session {
	return &capabilityWrapper{Session: inner}
}

func (w *capabilityWrapper) UpdateRow(table string, pk, changes map[string]*string) error {
	if u, ok := w.Session.(Updater); ok {
		return u.UpdateRow(table, pk, changes)
	}
	return errUnsupported("update")
}

func (w *capabilityWrapper) DeleteRow(table string, pk map[string]*string) error {
	if u, ok := w.Session.(Updater); ok {
		return u.DeleteRow(table, pk)
	}
	return errUnsupported("delete")
}

func (w *capabilityWrapper) InsertRow(table string, values map[string]*string) error {
	if u, ok := w.Session.(Updater); ok {
		return u.InsertRow(table, values)
	}
	return errUnsupported("insert")
}

func (w *capabilityWrapper) ApplyChanges(table string, updates []RowEdit, deletes []map[string]*string, inserts []map[string]*string, ddl []string) error {
	if b, ok := w.Session.(BatchUpdater); ok {
		return b.ApplyChanges(table, updates, deletes, inserts, ddl)
	}
	return errUnsupported("apply changes")
}

func (w *capabilityWrapper) KillQuery() error {
	if c, ok := w.Session.(Canceller); ok {
		return c.KillQuery()
	}
	return errUnsupported("kill query")
}

func (w *capabilityWrapper) CreateDatabase(name string, opts map[string]string) error {
	if c, ok := w.Session.(DatabaseCreator); ok {
		return c.CreateDatabase(name, opts)
	}
	return errUnsupported("create database")
}

func (w *capabilityWrapper) Begin() error {
	if t, ok := w.Session.(Transactional); ok {
		return t.Begin()
	}
	return errUnsupported("transaction")
}

func (w *capabilityWrapper) Commit() error {
	if t, ok := w.Session.(Transactional); ok {
		return t.Commit()
	}
	return errUnsupported("commit")
}

func (w *capabilityWrapper) Rollback() error {
	if t, ok := w.Session.(Transactional); ok {
		return t.Rollback()
	}
	return errUnsupported("rollback")
}

func (w *capabilityWrapper) InTransaction() bool {
	if t, ok := w.Session.(Transactional); ok {
		return t.InTransaction()
	}
	return false
}

func (w *capabilityWrapper) QueryStream(sqlText string, batch int, emit func(cols []proto.ColumnInfo, rows [][]*string) (bool, error)) (proto.QueryResult, error) {
	if st, ok := w.Session.(Streamer); ok {
		return st.QueryStream(sqlText, batch, emit)
	}
	return proto.QueryResult{}, errUnsupported("streaming")
}

func (w *capabilityWrapper) Metrics() ([]proto.Metric, error) {
	if m, ok := w.Session.(Metricser); ok {
		return m.Metrics()
	}
	return nil, errUnsupported("metrics")
}

// errUnsupported is returned when a wrapped session is asked to perform a
// capability its inner session does not implement.
func errUnsupported(what string) error {
	return fmt.Errorf("this connection does not support %s", what)
}
