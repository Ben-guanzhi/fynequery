package main

// SQL 代码片段库（对齐 Navicat 片段 / DataGrip live templates；dbstudio 的
// snippet.go 同一语义在 Fyne 形态的落地）：
//
// 与历史收藏的区别——历史/收藏绑定「当时的连接」，收藏还是历史条目的标记
// （名字=SQL 首行自动生成，存不了没跑过的 SQL）；片段是**用户自己命名的、
// 跨连接复用的 SQL 块**，可以先写好存起来，之后在任何连接里载入。
//
// 存储：snippets.json（configDir 下），0600，tmp+rename 原子写；名字唯一键
// （同名覆盖 = 更新），列表按名字排序。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// snippet 是一条用户命名的可复用 SQL 块。
type snippet struct {
	Name string    `json:"name"`
	SQL  string    `json:"sql"`
	At   time.Time `json:"at"`
}

// snippetErr 是片段库的错误（UI 直接显示）。
var errSnippetName = errors.New("片段名不能为空")

// snippetStore 持锁读写片段文件。
type snippetStore struct {
	mu    sync.Mutex
	path  string
	items []snippet
	// loadFailed 记录加载失败：坏文件在留档前不可被 saveLocked 覆盖。
	loadFailed bool
}

func newSnippetStore(dir string) *snippetStore {
	return &snippetStore{path: filepath.Join(dir, "snippets.json")}
}

// load 读盘；文件缺失/损坏视为空库（首次使用是正常路径）。
func (s *snippetStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.loadFailed = true // 文件在但读不了：后续保存必须先留档
		}
		s.items = nil
		return
	}
	var v struct {
		Snippets []snippet `json:"snippets"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		s.loadFailed = true
		s.items = nil
		return
	}
	s.items = v.Snippets
}

// list 返回按名字排序的副本。
func (s *snippetStore) list() []snippet {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]snippet(nil), s.items...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// put 保存片段：同名覆盖（= 更新），空白名拒绝。
func (s *snippetStore) put(sn snippet) error {
	sn.Name = strings.TrimSpace(sn.Name)
	if sn.Name == "" {
		return errSnippetName
	}
	sn.SQL = strings.TrimRight(sn.SQL, "\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	sn.At = time.Now()
	for i := range s.items {
		if s.items[i].Name == sn.Name {
			s.items[i] = sn
			return s.saveLocked()
		}
	}
	s.items = append(s.items, sn)
	return s.saveLocked()
}

// del 按名字删除；不存在报错。
func (s *snippetStore) del(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].Name == name {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return s.saveLocked()
		}
	}
	return errSnippetName
}

// saveLocked 原子落盘（tmp + rename）。调用方必须持锁。
func (s *snippetStore) saveLocked() error {
	// 解析失败的片段库不可覆盖：内存里的 items 不含旧片段，直接写 =
	// 用户积累的片段全部丢失（§339 同款闸门）。留档后放行重建。
	if s.loadFailed {
		if raw, err := os.ReadFile(s.path); err == nil {
			_ = os.WriteFile(s.path+".corrupt", raw, 0o600)
		}
		s.loadFailed = false
		return fmt.Errorf("snippets.json 解析失败，副本已存为 .corrupt——请手工修复后重启，或重建片段")
	}
	raw, err := json.MarshalIndent(struct {
		Snippets []snippet `json:"snippets"`
	}{s.items}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// snippetFirstLine 是列表里的单行预览。
func snippetFirstLine(sql string) string {
	first := strings.TrimSpace(strings.SplitN(sql, "\n", 2)[0])
	first = strings.ReplaceAll(first, "\t", " ")
	r := []rune(first)
	if len(r) > 44 {
		first = string(r[:44]) + "…"
	}
	return first
}
