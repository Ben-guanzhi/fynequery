package db

import (
	"fmt"
	"fynequery/proto"
	"time"
)

// unwrapper is implemented by every Session decorator (tunnel, pool, …) so the
// capability helpers below can see through arbitrary nesting to the real
// session.
type unwrapper interface {
	innerSession() Session
}

// asCapability is the shared engine behind every As* helper. It unwraps
// decorators to the innermost session before answering, because a
// capabilityWrapper UNCONDITIONALLY satisfies every capability interface and a
// bare type assertion would wrongly report editing support for a wrapped
// connection whose inner session is not editable.
func asCapability[T any](s Session) (T, bool) {
	var zero T
	c, ok := s.(T)
	if !ok {
		return zero, false
	}
	if w, ok := s.(unwrapper); ok {
		if inner, ok := asCapability[T](w.innerSession()); ok {
			return inner, true
		}
		return zero, false
	}
	return c, true
}

// AsUpdater reports whether s can edit rows, unwrapping any decorators.
func AsUpdater(s Session) (Updater, bool) { return asCapability[Updater](s) }

// AsBatchUpdater reports whether s can apply a batch of changes.
func AsBatchUpdater(s Session) (BatchUpdater, bool) { return asCapability[BatchUpdater](s) }

// AsValueWriter reports whether s can write KV elements in place.
func AsValueWriter(s Session) (ValueWriter, bool) { return asCapability[ValueWriter](s) }

// AsKeyAdmin reports whether s can inspect and delete/rename KV keys.
func AsKeyAdmin(s Session) (KeyAdmin, bool) { return asCapability[KeyAdmin](s) }

// AsValueDeleter reports whether s can delete single KV elements.
func AsValueDeleter(s Session) (ValueDeleter, bool) { return asCapability[ValueDeleter](s) }

// AsPinger reports whether s can check liveness with a native ping.
func AsPinger(s Session) (Pinger, bool) { return asCapability[Pinger](s) }

// AsKeyPager reports whether s can load keys beyond the first inventory page.
func AsKeyPager(s Session) (KeyPager, bool) { return asCapability[KeyPager](s) }

// AsCanceller reports whether s can cancel a running query.
func AsCanceller(s Session) (Canceller, bool) { return asCapability[Canceller](s) }

// AsDatabaseCreator reports whether s can create a database.
func AsDatabaseCreator(s Session) (DatabaseCreator, bool) { return asCapability[DatabaseCreator](s) }

// AsTransactional reports whether s supports a manual transaction.
func AsTransactional(s Session) (Transactional, bool) { return asCapability[Transactional](s) }

// AsStreamer reports whether s can stream query results incrementally.
func AsStreamer(s Session) (Streamer, bool) { return asCapability[Streamer](s) }

// AsMetricser reports whether s can report server metrics.
func AsMetricser(s Session) (Metricser, bool) { return asCapability[Metricser](s) }

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

func (w *capabilityWrapper) SetValue(edit ValueEdit) error {
	if v, ok := w.Session.(ValueWriter); ok {
		return v.SetValue(edit)
	}
	return errUnsupported("element writes")
}

func (w *capabilityWrapper) DeleteValue(edit ValueEdit) error {
	if v, ok := w.Session.(ValueDeleter); ok {
		return v.DeleteValue(edit)
	}
	return errUnsupported("element deletes")
}

func (w *capabilityWrapper) KeyMeta(key string) (KeyMeta, error) {
	if k, ok := w.Session.(KeyAdmin); ok {
		return k.KeyMeta(key)
	}
	return KeyMeta{}, errUnsupported("key administration")
}

func (w *capabilityWrapper) DeleteKey(key string) error {
	if k, ok := w.Session.(KeyAdmin); ok {
		return k.DeleteKey(key)
	}
	return errUnsupported("key administration")
}

func (w *capabilityWrapper) RenameKey(oldKey, newKey string) error {
	if k, ok := w.Session.(KeyAdmin); ok {
		return k.RenameKey(oldKey, newKey)
	}
	return errUnsupported("key administration")
}

func (w *capabilityWrapper) SetKeyTTL(key string, ttl time.Duration) error {
	if k, ok := w.Session.(KeyAdmin); ok {
		return k.SetKeyTTL(key, ttl)
	}
	return errUnsupported("key administration")
}

func (w *capabilityWrapper) Ping() error {
	if p, ok := w.Session.(Pinger); ok {
		return p.Ping()
	}
	return errUnsupported("ping")
}

func (w *capabilityWrapper) MoreKeys() bool {
	if p, ok := w.Session.(KeyPager); ok {
		return p.MoreKeys()
	}
	return false
}

func (w *capabilityWrapper) LoadMoreKeys() (int, bool, error) {
	if p, ok := w.Session.(KeyPager); ok {
		return p.LoadMoreKeys()
	}
	return 0, false, errUnsupported("key paging")
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
