package main

// 片段库坏文件守卫（§340）：snippets.json 解析失败后，put/del 触发的
// 保存必须先留档 .corrupt——片段是用户积累的创作内容，静默覆盖 = 全丢。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnippetCorruptFileArchivedBeforeOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snippets.json")
	if err := os.WriteFile(path, []byte(`{"snippets":[{"name":"lost"`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSnippetStore(dir)
	s.load()

	// put 一条新片段：保存被拒绝（留档），错误可操作。
	err := s.put(snippet{Name: "rebuilt", SQL: "SELECT 1"})
	if err == nil || !strings.Contains(err.Error(), ".corrupt") {
		t.Fatalf("put must refuse with a .corrupt note, got %v", err)
	}
	if _, statErr := os.Stat(path + ".corrupt"); statErr != nil {
		t.Fatalf("the broken file must be archived: %v", statErr)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"lost"`) {
		t.Fatal("the broken content must stay in place")
	}

	// 留档后放行：重建的片段写进文件。
	if err := s.put(snippet{Name: "rebuilt", SQL: "SELECT 1"}); err != nil {
		t.Fatalf("the follow-up put should proceed: %v", err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "rebuilt") {
		t.Fatal("the follow-up put should carry the new snippet")
	}
}
