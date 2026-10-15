package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"tablepro-fyne/internal/db"
)

// These tests pin what happens while a query is in flight: the page bar must
// not start a second run, and 取消 must still belong to the run that is actually
// running.
//
// The bug they are written against was not a data race but an ordering one: a
// page turn during a run started a second tracked run, which overwrote
// Client.cancelFn. Cancellation then stopped only the newest run while the old
// one kept going, and whichever finish callback landed last got to paint the
// grid - so the result showed page A while the bar said page B.

// parkable swaps Client.dispatch for a channel the test drains by hand, so the
// worker goroutine is genuinely in flight until the test lets its result land.
func parkable(c *Client) chan func() {
	c.asyncRun = true
	queued := make(chan func(), 8)
	c.dispatch = func(f func()) { queued <- f }
	return queued
}

// instrumentedRun is one tracked run with its innards exposed as channels: a
// refused run never closes started, a cancelled one closes canceled.
type instrumentedRun struct {
	label    string
	started  chan struct{}
	canceled chan struct{}
	finished chan struct{}
	block    chan struct{}

	runOnce    sync.Once
	cancelSeen sync.Once
}

func newInstrumentedRun(label string) *instrumentedRun {
	return &instrumentedRun{
		label:    label,
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		finished: make(chan struct{}),
		block:    make(chan struct{}),
	}
}

// unblock releases a run whose work is still parked. Tests register it as
// cleanup so a failing assertion can never leave the worker goroutine blocked
// until Go's test deadline fires.
func (r *instrumentedRun) unblock() { r.runOnce.Do(func() { close(r.block) }) }

// waitFinished reports once the work has returned, unblocking it first if the
// run is still parked - which is itself a failure the caller has already
// recorded through the canceled channel.
func (r *instrumentedRun) waitFinished(t *testing.T) {
	t.Helper()
	select {
	case <-r.finished:
	case <-time.After(2 * time.Second):
		r.unblock()
		<-r.finished
	}
}

// start asks the client for this run and returns immediately: in async mode the
// work goes to a goroutine and the finish callback waits in the dispatch queue.
func (r *instrumentedRun) start(c *Client) {
	c.runTracked("运行中："+r.label,
		func(ctx context.Context) (db.QueryResult, string) {
			close(r.started)
			select {
			case <-r.block:
			case <-ctx.Done():
				r.cancelSeen.Do(func() { close(r.canceled) })
			}
			close(r.finished)
			return db.QueryResult{RowCount: 0}, r.label
		},
		func(res db.QueryResult, _ string, _ bool) {})
}

// TestRunTrackedRefusesASecondRun is the ordering guard aimed at the runner
// itself, with no database in the way: while a run is in flight a second one
// must not start, and the cancellation must stay attached to the first.
func TestRunTrackedRefusesASecondRun(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("t")
	c := newTestClient(a, w)
	w.SetContent(c.buildUI())
	queued := parkable(c)

	first := newInstrumentedRun("first")
	t.Cleanup(first.unblock)
	first.start(c)
	<-first.started

	// A page turn, a size change or a second 执行 all land here mid-run.
	second := newInstrumentedRun("second")
	t.Cleanup(second.unblock)
	second.start(c)
	select {
	case <-second.started:
		t.Error("a second tracked run started while the first was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	if !strings.Contains(c.status.Text, "已有查询") {
		t.Errorf("the refused run left the status bar saying %q, want the reason", c.status.Text)
	}

	// 取消 has to stop the run that is really running. Overwriting cancelFn made
	// it stop the refused one and leave this one going.
	c.cancelRun()
	select {
	case <-first.canceled:
	case <-time.After(2 * time.Second):
		t.Error("取消 no longer stopped the in-flight run")
	}
	first.waitFinished(t)
	land(t, queued)

	// Once the running round is over, the next one is allowed again.
	third := newInstrumentedRun("third")
	t.Cleanup(third.unblock)
	third.start(c)
	select {
	case <-third.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the guard still refuses runs after the in-flight one finished")
	}
	third.unblock()
	third.waitFinished(t)
	land(t, queued)
}

// land waits for the in-flight run to hand its result back and then runs the
// finish callback, the way the UI thread would. The worker goroutine returns
// before it dispatches, so this must wait rather than poll the channel once.
func land(t *testing.T, queued chan func()) {
	t.Helper()
	select {
	case f := <-queued:
		f()
	case <-time.After(5 * time.Second):
		t.Fatal("a run never handed its result back to the UI thread")
	}
}

// TestPagingBarLocksWhileARunIsInFlight is the same guard through the real
// widgets: the page bar describes the result on screen, so while a run is going
// 上一页/下一页/每页/分页浏览 must be out of reach - and the call behind them must
// be refused rather than starting a second query.
func TestPagingBarLocksWhileARunIsInFlight(t *testing.T) {
	c := newPagingClient(t, 25, true, "10")
	queued := parkable(c)

	c.tabs.entry.setTextSilently(`SELECT id, name FROM items ORDER BY id`)
	c.runCurrent()

	// Both turn buttons are locked while a run is in flight. nextBtn is the one
	// that would otherwise be live (page 1 of 25 rows at 10 per page), so it is
	// the assertion with teeth; prevBtn is checked alongside it because the two
	// are meant to move together.
	if !isDisabled(c.page.prevBtn) || !isDisabled(c.page.nextBtn) {
		t.Error("the turn buttons stayed clickable while a run was in flight")
	}
	if !isDisabled(c.page.sizeSel) {
		t.Error("每页 stayed selectable while a run was in flight")
	}
	if !isDisabled(c.page.check) {
		t.Error("分页浏览 stayed clickable while a run was in flight")
	}

	// The guard behind the widgets, for a caller a greyed-out button cannot stop
	// (a keyboard shortcut, a stale callback).
	before := c.page.no
	c.runPage(2)
	if !strings.Contains(c.status.Text, "已有查询") {
		t.Errorf("a page turn during a run left the status bar saying %q", c.status.Text)
	}
	if c.page.no != before {
		t.Errorf("the refused turn still moved the page number to %d", c.page.no)
	}

	// Let the real run land: the bar redraws for the page it fetched and every
	// control is reachable again.
	land(t, queued)
	if isDisabled(c.runBtn) {
		t.Fatal("the run button stayed disabled after the run finished")
	}
	if isDisabled(c.page.nextBtn) {
		t.Error("the turn buttons stayed locked after the run finished")
	}
	if isDisabled(c.page.sizeSel) || isDisabled(c.page.check) {
		t.Error("the size selector or the paging switch stayed locked after the run finished")
	}
	if c.page.no != 1 {
		t.Errorf("page number after the landed run = %d, want 1", c.page.no)
	}
}

// isDisabled reads the disabled flag off any Fyne widget that has one, without
// the test having to know which of them expose it as a field and which as a
// method.
func isDisabled(w interface{}) bool {
	if d, ok := w.(interface{ Disabled() bool }); ok {
		return d.Disabled()
	}
	if d, ok := w.(interface{ IsDisabled() bool }); ok {
		return d.IsDisabled()
	}
	// A widget the test cannot interrogate at all would report "enabled" forever,
	// so say loudly that this helper needs extending.
	panic("isDisabled: widget type exposes no disabled state")
}
