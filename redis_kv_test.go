package main

// KV 键视图(建议⑥): Redis 会话的数据页退出"表形伪装"——结构/索引 segment、
// Filter/Columns/≡ 按钮、列结构详情全部退场,换上键元数据与键专用占位符。

import (
	"strings"
	"testing"
	"time"

	"fynequery/db"
)

func TestKVDetailsFields(t *testing.T) {
	// 永不过期: HasTTL=false
	fs := kvDetailsFields("myhash", &db.KeyMeta{Kind: "hash"}, 128, nil, nil)
	if got := fieldText(t, fs, "类型"); got != "hash" {
		t.Fatalf("类型 = %q, want hash", got)
	}
	if got := fieldText(t, fs, "TTL"); got != "永不过期" {
		t.Fatalf("TTL = %q, want 永不过期", got)
	}
	if got := fieldText(t, fs, "元素数(本窗口)"); got != "128" {
		t.Fatalf("元素数 = %q, want 128", got)
	}
	if got := fieldText(t, fs, "键"); got != "myhash" {
		t.Fatalf("键 = %q, want myhash", got)
	}

	// 有 TTL: 剩余时长可读
	fs = kvDetailsFields("k", &db.KeyMeta{Kind: "string", HasTTL: true, TTL: 90 * time.Second}, 1, nil, nil)
	if got := fieldText(t, fs, "TTL"); got != "剩余 1m30s" {
		t.Fatalf("TTL = %q, want 剩余 1m30s", got)
	}

	// HasTTL 且剩余 0: 已过期
	fs = kvDetailsFields("k", &db.KeyMeta{Kind: "string", HasTTL: true, TTL: 0}, 0, nil, nil)
	if got := fieldText(t, fs, "TTL"); got != "已过期" {
		t.Fatalf("TTL = %q, want 已过期", got)
	}

	// 元数据未取到: 类型未知但界面不留空
	fs = kvDetailsFields("k", nil, 0, nil, nil)
	if got := fieldText(t, fs, "类型"); got != "未知" {
		t.Fatalf("类型 = %q, want 未知", got)
	}
	if got := fieldText(t, fs, "TTL"); got != "永不过期" {
		t.Fatalf("TTL = %q, want 永不过期", got)
	}
}

func fieldText(t *testing.T, fs []detailsField, name string) string {
	t.Helper()
	for _, f := range fs {
		if f.Name == name {
			if f.Value == nil {
				return "<nil>"
			}
			return *f.Value
		}
	}
	t.Fatalf("field %q missing", name)
	return ""
}

// TestQueryPlaceholderRedis 命令面板: Redis 会话的编辑器占位符教的是命令而非 SQL。
func TestQueryPlaceholderRedis(t *testing.T) {
	if p := queryPlaceholder("redis"); !strings.Contains(p, "Redis") || strings.Contains(p, "SELECT id, name") {
		t.Fatalf("redis placeholder = %q, want a command example", p)
	}
	if p := queryPlaceholder("mysql"); !strings.Contains(p, "SELECT id, name") {
		t.Fatalf("sql placeholder = %q, want the SQL example", p)
	}
}

// TestKVChromeHiddenForRedis 专用键视图: KV 会话的数据页隐藏结构/索引 segment
// 与 SQL 专属按钮(Filter/Columns/≡),退回 SQL 连接时全部恢复。
func TestKVChromeHiddenForRedis(t *testing.T) {
	ws := newTabHarness()
	ws.pageMarkBtn = newFlatButton("≡")
	ws.columnsBtn = newFlatButton("Columns")
	// 生产代码在 buildBottomBar 构建删除键按钮;测试里手工补齐,否则断言空指针。
	ws.deleteKeyBtn = newFlatButton("删除键")
	tab := &dataTableTab{name: "mykey"}
	tab.grid = newDataTable(emptyTableData(), ws.status)
	ws.dataTabs = append(ws.dataTabs, tab)

	// SQL 连接:完整 chrome 可见。
	ws.conn.Driver = "mysql"
	ws.applyDataTab(tab)
	if !ws.segment.Visible() {
		t.Fatal("SQL data view must show the segment control")
	}
	if !ws.filterBtn.Visible() || !ws.columnsBtn.Visible() || !ws.pageMarkBtn.Visible() {
		t.Fatal("SQL data view must show Filter/Columns/≡")
	}
	if ws.deleteKeyBtn.Visible() {
		t.Fatal("SQL data view must not show 删除键")
	}

	// Redis:伪装退场。
	ws.conn.Driver = "redis"
	ws.applyDataTab(tab)
	if ws.segment.Visible() {
		t.Fatal("KV keys must not show the Structure/Indexes segments")
	}
	if ws.filterBtn.Visible() || ws.columnsBtn.Visible() || ws.pageMarkBtn.Visible() {
		t.Fatal("KV keys must not show SQL-only buttons")
	}
	if !ws.deleteKeyBtn.Visible() {
		t.Fatal("KV keys must show 删除键")
	}
	if tab.segIdx != 0 {
		t.Fatalf("KV view must pin to the Data segment, segIdx = %d", tab.segIdx)
	}

	// 切回 SQL 连接:chrome 恢复。
	ws.conn.Driver = "mysql"
	ws.applyDataTab(tab)
	if !ws.segment.Visible() || !ws.filterBtn.Visible() {
		t.Fatal("switching back to SQL must restore the chrome")
	}
	if ws.deleteKeyBtn.Visible() {
		t.Fatal("switching back to SQL must hide 删除键")
	}
}

// TestDeleteActiveKeyGuards 删除键的守卫: 非 KV 连接与空数据页都直接返回,
// 不 panic、不弹确认框。
func TestDeleteActiveKeyGuards(t *testing.T) {
	ws := newTabHarness()
	ws.conn.Driver = "mysql"
	ws.deleteActiveKey() // 非 KV: no-op

	ws.conn.Driver = "redis"
	ws.activeData = nil
	ws.deleteActiveKey() // 无数据页: no-op
}

// TestTTLParseSeconds 钉住 TTL 输入解析: 裸秒数、Go 时长拼写、0(永不过期)
// 都接受;负数、垃圾、非正时长报错。
func TestTTLParseSeconds(t *testing.T) {
	ok := map[string]int64{
		"30": 30, "0": 0, "  45  ": 45,
		"30s": 30, "5m": 300, "1h30m": 5400, "90": 90,
	}
	for in, want := range ok {
		got, err := ttlParseSeconds(in)
		if err != nil || got != want {
			t.Errorf("ttlParseSeconds(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"-5", "abc", "", "1h30m ago"} {
		if _, err := ttlParseSeconds(in); err == nil {
			t.Errorf("ttlParseSeconds(%q) = nil error, want failure", in)
		}
	}
}
