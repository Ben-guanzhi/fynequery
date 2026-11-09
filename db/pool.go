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
	"fmt"
	"sync"
	"time"
)

const (
	defaultPoolMaxIdle = 16
	defaultPoolIdleTTL = 10 * time.Minute
	// defaultMaxTotalSessions bounds the total number of live sessions across
	// the whole pool. Without it, opening many tabs on one connection spawns
	// an unbounded number of concurrent sessions (plugin processes or DB
	// pools), each with its own server connections. This is the admission
	// control that keeps the system from entering oscillation under load.
	defaultMaxTotalSessions = 64
)

// poolAcquireTimeout bounds how long Get waits for a slot in the global
// semaphore before failing. A UI client should not hang forever on a full
// pool. A var, not a const, so the test can fail fast instead of sleeping
// through the real production timeout.
var poolAcquireTimeout = 30 * time.Second

// pooled is one cached real session.
type pooled struct {
	sess   Session
	fp     string
	ref    int       // 0 = idle (available for reuse), >0 = checked out
	idleAt time.Time // when it became idle (used for LRU/ TTL)
	dead   bool      // evicted; do not reuse, Close is a no-op
}

// closeGroup is the joinable group FQ-04 requires for the asynchronous eviction
// closes removeLocked spawns. The old shape fired
// `go func(){ e.sess.Close(); <-p.sem }()` and moved on, so a later CloseAll
// (or process exit) could return while a session's Close was still in flight,
// leaking the server-side socket and the plugin process behind it. add and wait
// share one mutex, so a close started just before wait() cannot be missed.
type closeGroup struct {
	mu   sync.Mutex
	cond *sync.Cond
	n    int
}

func newCloseGroup() *closeGroup {
	c := &closeGroup{}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *closeGroup) add() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *closeGroup) done() {
	c.mu.Lock()
	if c.n > 0 {
		c.n--
	}
	if c.n == 0 {
		c.cond.Broadcast()
	}
	c.mu.Unlock()
}

// wait blocks until every close started through add has finished.
func (c *closeGroup) wait() {
	c.mu.Lock()
	for c.n > 0 {
		c.cond.Wait()
	}
	c.mu.Unlock()
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
	// sem is a global admission gate: each live session holds one slot. A new
	// open cannot proceed when the gate is full, so the total server-connection
	// footprint is bounded even when many tabs are open.
	sem      chan struct{}
	maxTotal int
	// closeGroup tracks the asynchronous eviction closes removeLocked spawns so
	// CloseAll can join them before returning (FQ-04).
	closeGroup *closeGroup
}

// NewSessionPool builds a pool. maxIdle<=0 defaults to 16; idleTTL<=0 defaults
// to 10 minutes; maxTotal<=0 defaults to 64 live sessions.
func NewSessionPool(maxIdle int, idleTTL time.Duration, maxTotal int) *SessionPool {
	if maxIdle <= 0 {
		maxIdle = defaultPoolMaxIdle
	}
	if idleTTL <= 0 {
		idleTTL = defaultPoolIdleTTL
	}
	if maxTotal <= 0 {
		maxTotal = defaultMaxTotalSessions
	}
	return &SessionPool{
		maxIdle:  maxIdle,
		idleTTL:  idleTTL,
		byFP:     map[string][]*pooled{},
		sem:      make(chan struct{}, maxTotal),
		maxTotal: maxTotal,
		closeGroup: newCloseGroup(),
	}
}

// DefaultPool is the process-wide pool used by dialConn.
var DefaultPool = NewSessionPool(defaultPoolMaxIdle, defaultPoolIdleTTL, defaultMaxTotalSessions)

// Get returns a Session for fp. It reuses an idle, non-expired cached session
// when one exists; otherwise it calls open to create a new one. The returned
// session's Close releases it back to the pool.
//
// A global semaphore admits new opens: when every slot is taken, Get waits up
// to poolAcquireTimeout for one to free, then fails. This is the admission
// control that keeps the total server-connection footprint bounded.
func (p *SessionPool) Get(fp string, open func() (Session, error)) (Session, error) {
	p.mu.Lock()
	if list := p.byFP[fp]; list != nil {
		// Backward scan: a stale entry is removed in place, and continuing
		// the scan must not skip the entries that shift after it — the old
		// `break` abandoned every still-warm session behind the first stale
		// one and dialed a brand-new session instead.
		for i := len(list) - 1; i >= 0; i-- {
			e := list[i]
			if e.ref == 0 && !e.dead {
				if time.Since(e.idleAt) > p.idleTTL {
					// Stale: drop it and keep looking for a warm session.
					p.removeLocked(fp, i)
					continue
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

	// Admission control: acquire a slot before opening a new session.
	select {
	case p.sem <- struct{}{}:
	case <-time.After(poolAcquireTimeout):
		return nil, fmt.Errorf("session pool: max %d live sessions reached", p.maxTotal)
	}

	inner, err := open()
	if err != nil {
		// Release the slot since the open failed.
		<-p.sem
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
//
// A session CloseAll marked dead while it was checked out settles here: the
// entry can never be handed out again, so the last release closes the inner
// session and returns its admission slot — the old shape leaked both (the
// slot permanently and the session's server-side resources until the process
// died).
func (p *SessionPool) release(e *pooled) {
	p.mu.Lock()
	if e.ref > 0 {
		e.ref--
	}
	if e.ref > 0 {
		p.mu.Unlock()
		return
	}
	if e.dead {
		// Checked-out when CloseAll ran: the pool no longer tracks this
		// entry (it was removed from byFP), so the last user closes it.
		p.mu.Unlock()
		e.sess.Close()
		<-p.sem
		return
	}
	e.idleAt = time.Now()
	p.idleCnt++
	p.evictIfNeededLocked()
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
// p.mu. Releases the global semaphore slot when the session is actually closed.
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
		// FQ-04: the eviction close runs off the pool lock but inside the
		// joinable group, so CloseAll/Shutdown can wait for it instead of
		// racing it (the old shape fired the goroutine and moved on).
		p.closeGroup.add()
		go func() {
			defer p.closeGroup.done()
			e.sess.Close()
			// Release the global admission slot: the session is gone.
			<-p.sem
		}()
	}
}

// CloseAll closes every cached idle session and empties the pool.
//
// Sessions that are checked out (ref > 0) are NOT closed here: their owner is
// still running statements on them, and yanking the session from underneath
// failed in-flight work. They are marked dead instead — release() closes the
// inner session and returns the admission slot when the last user finishes
// (see release). Before this, a shutdown racing an in-flight query both killed
// the query mid-statement and leaked its admission slot forever.
func (p *SessionPool) CloseAll() {
	p.mu.Lock()
	var toClose []Session
	for fp, list := range p.byFP {
		kept := list[:0]
		for _, e := range list {
			if e.dead {
				continue // already settled; release() owns the slot
			}
			if e.ref > 0 {
				// Checked out: mark dead so it can never be reused, but let
				// release() do the closing once the owner is done.
				e.dead = true
				kept = append(kept, e)
				continue
			}
			e.dead = true
			toClose = append(toClose, e.sess)
		}
		if len(kept) == 0 {
			delete(p.byFP, fp)
		} else {
			p.byFP[fp] = kept
		}
	}
	// idleCnt only counted ref==0 entries (see release/removeLocked), so the
	// entries being dropped here are exactly the ones it tracks.
	p.idleCnt = 0
	p.mu.Unlock()
	for _, s := range toClose {
		s.Close()
		<-p.sem
	}
	// FQ-04: join every asynchronous eviction close removeLocked started before
	// returning, so no session's Close (and its server-side socket / plugin
	// process) is still in flight when the caller believes the pool is empty.
	// The p.mu handoff serializes this with removeLocked's closeGroup.add, so a
	// close started just before the wait cannot be missed.
	p.waitForEvictionCloses()
}

// waitForEvictionCloses blocks until every asynchronous eviction close started
// by removeLocked has finished. It takes p.mu first so that no further
// removeLocked can register a close while it is deciding; the closes themselves
// run outside p.mu and never need it.
func (p *SessionPool) waitForEvictionCloses() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeGroup.wait()
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
