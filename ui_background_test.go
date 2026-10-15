package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
)

// Client.background is the primitive item ⑥ added: every catalog reader now
// takes a context, so each UI caller needs a thread to run it on and a way to
// give up on it. The bug it is written against is that these reads used to run
// synchronously on the Fyne event thread with no context at all - on a slow
// remote server that froze the window, and 取消 could not help because the
// click was never delivered.
//
// The four tests below pin the contract that freeze was measured against: the
// round trip leaves the caller's thread, giving up mid-flight reports nothing,
// a round trip that already made its decision still reports, and a test-mode
// client stays inline so its assertions need no waiting.
//
// Each one drives the hand-owned event thread through parkable, and every wait
// is a timeout that reports a failure rather than holding until Go's test
// deadline - so neutering the guard under a negative control reddens the test
// instead of hanging the package.

// awaitStart calls background from a goroutine the test does not depend on and
// hands back its cancel. A background that ran the round trip on the caller's
// thread parks here, so the test releases the parked work and reports that as
// the failure it is.
func awaitStart(t *testing.T, c *Client, round *parkedRound, work func(context.Context), after func()) func() {
	t.Helper()
	returns := make(chan func(), 1)
	go func() { returns <- c.background(work, after) }()
	select {
	case giveUp := <-returns:
		return giveUp
	case <-time.After(2 * time.Second):
		round.release()
		<-returns
		t.Fatal("background ran the round trip on the caller's thread: the window would be frozen")
	}
	return nil
}

// takeCallback waits for the round trip to hand its finish callback to the
// event thread without running it yet. Receiving it is also the proof that the
// worker had already decided whether the round was interrupted, so a test can
// cancel afterwards without racing the guard it exercises.
func takeCallback(t *testing.T, queued chan func()) func() {
	t.Helper()
	select {
	case f := <-queued:
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("the round trip never handed its callback to the event thread")
	}
	return nil
}

// parkedRound is one background round trip with its phases exposed as channels.
type parkedRound struct {
	started  chan struct{}
	released chan struct{}
	returned chan struct{}
	once     sync.Once
}

func newParkedRound() *parkedRound {
	return &parkedRound{
		started:  make(chan struct{}),
		released: make(chan struct{}),
		returned: make(chan struct{}),
	}
}

// release lets a parked round trip return. Tests call it from cleanup, so the
// once makes an explicit release in a test body and the fallback release the
// same event rather than a double close.
func (r *parkedRound) release() { r.once.Do(func() { close(r.released) }) }

// block parks the work until the test lets it go, or until the caller gives up
// on it - whichever comes first. Either way the round trip returns, so a failing
// assertion cannot leave the worker goroutine held.
func (r *parkedRound) block(ctx context.Context) {
	close(r.started)
	select {
	case <-r.released:
	case <-ctx.Done():
	}
	close(r.returned)
}

// TestBackgroundReturnsWhileTheRoundTripIsParked is the anti-freeze guard: the
// caller gets back control while the server round trip is still out, and the
// answer only reaches the event thread once the work has returned.
func TestBackgroundReturnsWhileTheRoundTripIsParked(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	queued := parkable(c)

	round := newParkedRound()
	t.Cleanup(round.release)

	giveUp := awaitStart(t, c, round, round.block, nil)
	if giveUp == nil {
		t.Error("background returned no way to give up on the round trip")
	}

	<-round.started
	select {
	case f := <-queued:
		if f != nil {
			f()
		}
		t.Error("the round trip handed its result back while it was still supposed to be parked")
	default:
	}

	// Let it finish: the answer reaches the event thread through the queue.
	round.release()
	takeCallback(t, queued)
}

// TestCanceledRoundTripReportsNothing pins the half of the contract the 取消
// button depends on: an answer the user stopped caring about must not land on
// the screen. The driver answers after all - the round trip just noticed it had
// been given up on before its finish callback ran.
func TestCanceledRoundTripReportsNothing(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	queued := parkable(c)

	round := newParkedRound()
	t.Cleanup(round.release)

	var reported int
	giveUp := awaitStart(t, c, round, round.block, func() { reported++ })

	<-round.started
	giveUp()
	<-round.returned
	takeCallback(t, queued)()

	if reported != 0 {
		t.Errorf("a round trip the caller gave up on still reported %d times, want 0", reported)
	}
}

// TestFinishedRoundTripStillReports is the ordering rule cancellation must not
// break: giving up after the answer is already produced still lets that answer
// land. Dropping it is how a dialog lost the result its caller was waiting on.
func TestFinishedRoundTripStillReports(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	queued := parkable(c)

	var reported int
	giveUp := c.background(func(context.Context) {}, func() { reported++ })

	callback := takeCallback(t, queued)
	giveUp()
	callback()

	if reported != 1 {
		t.Errorf("a finished round trip reported %d times after a cancel, want 1", reported)
	}
}

// TestBackgroundStaysInlineWithoutAsyncRun is the determinism half: a test-mode
// client (asyncRun off) has run both the work and its follow-up by the time the
// call returns, so every existing inline test keeps its synchronous shape.
// Making that path a goroutine would leave the whole suite order-dependent.
func TestBackgroundStaysInlineWithoutAsyncRun(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)

	var order []string
	c.background(func(context.Context) { order = append(order, "work") },
		func() { order = append(order, "after") })

	if len(order) != 2 || order[0] != "work" || order[1] != "after" {
		t.Errorf("inline background did %v, want [work after]", order)
	}
}
