package main

// 片段库行为测试（zz_ 前缀：Go 按文件名排序注册测试，新文件排在前面会打乱
// 既有测试依赖的初始化顺序——redis_kv_test 的教训）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnippetStorePutListDelete(t *testing.T) {
	dir := t.TempDir()
	s := newSnippetStore(dir)
	s.load()

	if err := s.put(snippet{Name: "  "}); err == nil {
		t.Fatal("空白名应当被拒绝")
	}

	if err := s.put(snippet{Name: "count-all", SQL: "SELECT count(*) FROM t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.put(snippet{Name: "users-by-id", SQL: "SELECT * FROM users WHERE id = 42"}); err != nil {
		t.Fatal(err)
	}
	list := s.list()
	if len(list) != 2 || list[0].Name != "count-all" || list[1].Name != "users-by-id" {
		t.Fatalf("列表顺序不对: %+v", list)
	}

	// 同名覆盖。
	if err := s.put(snippet{Name: "count-all", SQL: "SELECT count(*) FROM u"}); err != nil {
		t.Fatal(err)
	}
	if list := s.list(); len(list) != 2 || list[0].SQL != "SELECT count(*) FROM u" {
		t.Fatalf("同名覆盖失败: %+v", list)
	}

	if err := s.del("count-all"); err != nil {
		t.Fatal(err)
	}
	if err := s.del("count-all"); err == nil {
		t.Fatal("删除不存在的片段应当报错")
	}

	// 回读一致。
	s2 := newSnippetStore(dir)
	s2.load()
	if list := s2.list(); len(list) != 1 || list[0].SQL != "SELECT * FROM users WHERE id = 42" {
		t.Fatalf("回读不一致: %+v", list)
	}
}

// 文件 0600、原子落盘（无 .tmp 残片）。
func TestSnippetFileAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	s := newSnippetStore(dir)
	s.load()
	if err := s.put(snippet{Name: "n", SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "snippets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "SELECT 1") {
		t.Fatal("内容未落盘")
	}
	if _, err := os.Stat(filepath.Join(dir, "snippets.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("tmp 残片仍在——落盘不是原子的")
	}
}
