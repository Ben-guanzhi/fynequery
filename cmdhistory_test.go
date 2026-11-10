package main

// Command-history persistence (波55): executed statements survive a restart.

import (
	"os"
	"strconv"
	"testing"
)

func TestCommandHistoryRoundTrip(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	entries := []historyRec{
		{Stmt: "SELECT 1", Conn: "dev"},
		{Stmt: "SET k v", Conn: "dev"},
		{Stmt: "SELECT 'a;b'"},
	}
	if err := saveCommandHistory(commandHistoryPath(), entries); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := loadCommandHistory()
	if len(got) != len(entries) {
		t.Fatalf("loaded %d entries, want %d: %v", len(got), len(entries), got)
	}
	for i := range entries {
		if got[i] != entries[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], entries[i])
		}
	}
}

// 损坏/截断的文件必须静默降级为空历史,不得 panic 或弹错(历史是便利功能)。
func TestCommandHistoryCorruptFile(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	for _, content := range []string{"{not json", "", "{\"entries\": [}"} {
		if err := os.WriteFile(commandHistoryPath(), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := loadCommandHistory(); got != nil {
			t.Fatalf("corrupt file %q must load as nil, got %v", content, got)
		}
	}
}

// 缺失文件: 空历史,不报错。
func TestCommandHistoryMissingFile(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	if got := loadCommandHistory(); got != nil {
		t.Fatalf("missing file must load as nil, got %v", got)
	}
}

// 有界: 追加超过上限后,最老的先丢弃,内存与磁盘都不会无限膨胀。
func TestCommandHistoryCap(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	s := newSidebar(nil, nil)
	for i := 0; i < maxCommandHistory+50; i++ {
		s.appendHistory("SELECT "+itoa(i), "")
	}
	if len(s.history) != maxCommandHistory {
		t.Fatalf("history must be capped at %d, got %d", maxCommandHistory, len(s.history))
	}
	// 最老的 50 条被丢弃: 首条应是第 50 次执行的语句。
	if s.history[0].Stmt != "SELECT 50" {
		t.Fatalf("oldest entries must be dropped, got %q", s.history[0])
	}
	if s.history[len(s.history)-1].Stmt != "SELECT "+itoa(maxCommandHistory+49) {
		t.Fatalf("newest entry wrong, got %q", s.history[len(s.history)-1])
	}
}

// 侧栏构造时载入持久化的历史: 重启后 Ctrl+Up 仍能翻回上次的语句。
func TestCommandHistorySidebarLoads(t *testing.T) {
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	saveCommandHistory(commandHistoryPath(), []historyRec{{Stmt: "SELECT 1", Conn: ""}, {Stmt: "SELECT 2", Conn: ""}})
	s := newSidebar(nil, nil)
	if len(s.history) != 2 || s.history[0].Stmt != "SELECT 1" || s.history[1].Stmt != "SELECT 2" {
		t.Fatalf("sidebar must load persisted history, got %v", s.history)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
