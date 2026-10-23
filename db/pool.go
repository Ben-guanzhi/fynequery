package db

// SessionPool caches idle Sessions keyed by a connection fingerprint so that
// re-opening a recently used connection reuses the (expensive-to-spawn plugin
// process or freshly dialed) session instead of reconnecting from scratch.
// This mirrors the "always-on connection" behaviour of TablePlus and the
// remote project's pool.go, adapted to fynequery's process-isolated plugin
// model.
//
// An in-use session is NEVER shared: if every cached entry for a fingerprint is
// busy when Get is called, a fresh session is opened. Idle entries beyond
// MaxIdle (and any older than IdleTTL) are evicted LRU and closed.
//
// The Session returned by Get is a poolSession whose Close() releases the
// underlying connection back to the pool (it does not disconnect). The optional
// capability interfaces (Updater/BatchUpdater/Canceller/DatabaseCreator) are
// forwarded through the embedded capabilityWrapper, so pooling is transparent
// to editing / cancel / create.

import (
	"sync"
	"time"
)

const (
	defaultPoolMaxIdle = 16
	defaultPoolIdleTTL = 10 * time.Minute
)

// pooled is one cached real session.
type pooled struct {
	sess   Session
	fp     string
	ref    int       // 0 = idle (available for reuse), >0 = checked out
	idleAt time.Time // when it became idle (used for LRU/ TTL)
	dead   bool      // evicted; do not reuse, Close is a no-op
}

// poolSession wraps a pooled Session so that Close returns it to the pool.
type poolSession struct {
	*capabilityWrapper
	pool     *SessionPool
	ent      *pooled
	released bool
}

// Close releases the session back to the pool. It is idempotent. A transaction
// left open is rolled back first so the reused session starts clean.
func (s *poolSession) Close() {
	if s.released {
		return
	}
	s.released = true
	if tx, ok := AsTransactional(s.ent.sess); ok && tx.InTransaction() {
		_ = tx.Rollback()
	}
	s.pool.release(s.ent)
}

// SessionPool is a fingerprint-keyed cache of idle Sessions.
type SessionPool struct {
	mu      sync.Mutex
	maxIdle int
	idleTTL time.Duration
	byFP    map[string][]*pooled
	idleCnt int // total idle entries across all fingerprints (for the cap)
}

// NewSessionPool builds a pool. maxIdle<=0 defaults to 16; idleTTL<=0 defaults
// to 10 minutes.
func NewSessionPool(maxIdle int, idleTTL time.Duration) *SessionPool {
	if maxIdle <= 0 {
		maxIdle = defaultPoolMaxIdle
	}
	if idleTTL <= 0 {
		idleTTL = defaultPoolIdleTTL
	}
	return &SessionPool{
		maxIdle: maxIdle,
		idleTTL: idleTTL,
		byFP:    map[string][]*pooled{},
	}
}

// DefaultPool is the process-wide pool used by dialConn.
var DefaultPool = NewSessionPool(defaultPoolMaxIdle, defaultPoolIdleTTL)

// Get returns a Session for fp. It reuses an idle, non-expired cached session
// when one exists; otherwise it calls open to create a new one. The returned
// session's Close releases it back to the pool.
func (p *SessionPool) Get(fp string, open func() (Session, error)) (Session, error) {
	p.mu.Lock()
	if list := p.byFP[fp]; list != nil {
		for i, e := range list {
			if e.ref == 0 && !e.dead {
				if time.Since(e.idleAt) > p.idleTTL {
					// Stale: drop and fall through to open a fresh session.
					p.removeLocked(fp, i)
					break
				}
				e.ref = 1
				p.idleCnt--
				w := &poolSession{
					capabilityWrapper: &capabilityWrapper{Session: e.sess},
					pool:              p,
					ent:               e,
				}
				p.mu.Unlock()
				return w, nil
			}
		}
	}
	p.mu.Unlock()

	inner, err := open()
	if err != nil {
		return nil, err
	}
	e := &pooled{sess: inner, fp: fp, ref: 1}
	p.mu.Lock()
	p.byFP[fp] = append(p.byFP[fp], e)
	p.mu.Unlock()
	return &poolSession{
		capabilityWrapper: &capabilityWrapper{Session: inner},
		pool:              p,
		ent:               e,
	}, nil
}

// release checks a session back in (called by poolSession.Close).
func (p *SessionPool) release(e *pooled) {
	p.mu.Lock()
	if e.dead || e.ref <= 0 {
		p.mu.Unlock()
		return
	}
	e.ref--
	if e.ref == 0 {
		e.idleAt = time.Now()
		p.idleCnt++
		p.evictIfNeededLocked()
	}
	p.mu.Unlock()
}

// evictIfNeededLocked evicts the oldest idle entries until idleCnt<=maxIdle.
// Caller holds p.mu.
func (p *SessionPool) evictIfNeededLocked() {
	for p.idleCnt > p.maxIdle {
		var oldest *pooled
		var ofp string
		var oidx int
		found := false
		for fp, list := range p.byFP {
			for i, e := range list {
				if e.ref == 0 && !e.dead {
					if !found || e.idleAt.Before(oldest.idleAt) {
						oldest, ofp, oidx, found = e, fp, i, true
					}
				}
			}
		}
		if !found {
			return
		}
		p.removeLocked(ofp, oidx)
	}
}

// removeLocked removes an entry from its fingerprint list, adjusts idleCnt,
// marks it dead, and closes it in a goroutine (outside the lock). Caller holds
// p.mu.
func (p *SessionPool) removeLocked(fp string, idx int) {
	list := p.byFP[fp]
	e := list[idx]
	list = append(list[:idx], list[idx+1:]...)
	if len(list) == 0 {
		delete(p.byFP, fp)
	} else {
		p.byFP[fp] = list
	}
	if e.ref == 0 {
		p.idleCnt--
	}
	if !e.dead {
		e.dead = true
		go e.sess.Close()
	}
}

// CloseAll releases every cached session and empties the pool. Safe to call on
// shutdown.
func (p *SessionPool) CloseAll() {
	p.mu.Lock()
	var toClose []Session
	for _, list := range p.byFP {
		for _, e := range list {
			if !e.dead {
				e.dead = true
				toClose = append(toClose, e.sess)
			}
		}
	}
	p.byFP = map[string][]*pooled{}
	p.idleCnt = 0
	p.mu.Unlock()
	for _, s := range toClose {
		s.Close()
	}
}

// Len reports the number of cached entries (idle + checked-out). Test/debug.
func (p *SessionPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, list := range p.byFP {
		n += len(list)
	}
	return n
}
