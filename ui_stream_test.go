package main

// 流式渲染回归（P4 移植自内置版 query.go:480-506 的分批 + 代际守卫）。
// 阈值以下的结果不走这条路径（见 streamThreshold），所以既有测试不受影响。

import (
	"strconv"
	"tablepro-fyne/internal/db"
	"testing"
)

// bigRows 造一批超过流式阈值的行。
func bigRows(n int) [][]string {
	rows := make([][]string, n)
	for i := range rows {
		rows[i] = []string{strconv.Itoa(i)}
	}
	return rows
}

// TestRenderRowsStreamedAppendsEveryBatch pins the happy path: with an inline
// dispatcher every batch lands, so the grid ends up holding all rows.
func TestRenderRowsStreamedAppendsEveryBatch(t *testing.T) {
	c := newCrumbClient(t)
	c.dispatch = func(f func()) { f() } // 同步执行，便于断言
	c.grid.rows = nil

	c.renderRowsStreamed(bigRows(2500), "msg")
	if got := len(c.grid.rows); got != 2500 {
		t.Fatalf("流式渲染后行数 = %d, want 2500", got)
	}
}

// TestRenderRowsStreamedDropsStaleGeneration pins the reason the generation
// exists: once a newer run starts, the old result's batches must NOT land on
// top of it. 没有这个守卫，慢的大结果会盖掉已经取代它的那次查询。
func TestRenderRowsStreamedDropsStaleGeneration(t *testing.T) {
	c := newCrumbClient(t)
	called := 0
	c.dispatch = func(f func()) {
		called++
		if called == 1 {
			// 第一批还没落地就被新一轮取代（runTracked 里会 bump runGen）。
			c.mu.Lock()
			c.runGen++
			c.mu.Unlock()
		}
		f()
	}
	c.grid.rows = nil

	c.renderRowsStreamed(bigRows(2500), "msg")
	if got := len(c.grid.rows); got != 0 {
		t.Fatalf("过期代际的批次不应落地，实际行数 = %d", got)
	}
}

// TestSmallResultStaysSynchronous pins that results under the threshold keep
// the old one-shot behaviour — that is what every pre-existing test relies on.
func TestSmallResultStaysSynchronous(t *testing.T) {
	c := newCrumbClient(t)
	c.dispatch = func(f func()) { f() }

	n := streamThreshold - 1 // 刚好在阈值以下 → 仍走同步路径
	res := db.QueryResult{
		Columns:  []string{"n"},
		ColTypes: []string{"int"},
		Rows:     make([][]interface{}, n),
		RowCount: n,
	}
	for i := range res.Rows {
		res.Rows[i] = []interface{}{i}
	}
	c.renderResult(res)
	if got := len(c.grid.rows); got != n {
		t.Fatalf("小结果应一次性装载：行数 = %d, want %d", got, n)
	}
}
