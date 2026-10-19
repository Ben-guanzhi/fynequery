package main

// 索引页回归（P3）：钉住「详情 / 索引」切换与空态提示。
// 真正取索引需要连接（走 store.TableIndexes），留给 integration_test.go。

import (
	"testing"

	"tablepro-fyne/internal/db"
)

// TestDetailsIndexTabSwitchesMode pins the 详情/索引 tab switch: selecting the
// index tab flips the mode, and with no table open it explains itself instead
// of rendering an empty list.
func TestDetailsIndexTabSwitchesMode(t *testing.T) {
	c := newCrumbClient(t)
	c.toggleDetails()
	if c.details.tabs == nil {
		t.Fatal("详情/索引 标签未建")
	}
	if c.currentDetailsMode() != detailsModeInfo {
		t.Fatalf("初始模式 = %v, want 详情", c.currentDetailsMode())
	}

	c.details.tabs.SelectTabIndex(detailTabIndex)
	if c.currentDetailsMode() != detailsModeIndex {
		t.Fatalf("切到索引标签后模式 = %v, want 索引", c.currentDetailsMode())
	}
	c.refreshDetails() // 触发一次加载（无连接 → 空态提示）
	if got := len(c.details.indexBody.Objects); got != 1 {
		t.Fatalf("未打开表时索引页内容数 = %d, want 1（一条提示）", got)
	}

	c.details.tabs.SelectTabIndex(0)
	if c.currentDetailsMode() != detailsModeInfo {
		t.Fatalf("切回详情后模式 = %v, want 详情", c.currentDetailsMode())
	}
}

// TestIndexTableBuildsRows covers the renderer the index tab uses: PK and
// UNIQUE flags come out of db.IndexInfo the same way they do in the structure
// dialog, so the two views stay consistent.
func TestIndexTableBuildsRows(t *testing.T) {
	c := newCrumbClient(t)
	obj := c.indexTable([]db.IndexInfo{
		{Name: "PRIMARY", Kind: "BTREE", Primary: true, Columns: []string{"id"}},
		{Name: "idx_name", Kind: "BTREE", Unique: true, Columns: []string{"name"}},
		{Name: "idx_plain", Kind: "BTREE", Columns: []string{"qty"}},
	})
	if obj == nil {
		t.Fatal("索引表未生成")
	}
}

// TestDetailsIndexRefreshSkippedWhileHidden pins the guard: refreshing while
// the pane is closed must not touch the index body.
func TestDetailsIndexRefreshSkippedWhileHidden(t *testing.T) {
	c := newCrumbClient(t)
	c.details.tabs.SelectTabIndex(detailTabIndex)
	c.refreshDetails() // 面板还关着，应当直接返回
	if got := len(c.details.indexBody.Objects); got != 0 {
		t.Fatalf("面板隐藏时不应刷新索引页，实际内容数 = %d", got)
	}
}
