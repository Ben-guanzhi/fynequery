package main

// fynequery 客户端侧基准：每条 SQL 执行都要过的纯逻辑闸门
// （IsReadOnly 安全性判定 + IN 列表渲染），以及导入路径的 StripNoise。

import (
	"strings"
	"testing"

	"fynequery/internal/sqlscan"
)

// BenchmarkIsReadOnlyBulk：500 条典型语句过只读判定（safe gate 的
// 每次运行都付这个成本）。
func BenchmarkIsReadOnlyBulk(b *testing.B) {
	stmts := []string{
		"SELECT id, name FROM users WHERE active = 1 ORDER BY id LIMIT 100",
		"WITH recent AS (SELECT * FROM orders WHERE created_at > '2026-01-01') SELECT * FROM recent",
		"UPDATE users SET name = 'x' WHERE id = 1",
		"INSERT INTO logs (msg) VALUES ('hello')",
		"DELETE FROM sessions WHERE expired = 1",
		"SELECT /* prefix */ COUNT(*) FROM items WHERE price > 10",
	}
	// 放大到 500 条（20×30）。
	all := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		all = append(all, stmts[i%len(stmts)])
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, s := range all {
			_ = sqlscan.IsReadOnly(s)
		}
	}
}

// BenchmarkInListLiteral1000：IN 列表 1000 个值——过滤对话框构造
// WHERE col IN (...) 的渲染路径，含引号转义。
func BenchmarkInListLiteral1000(b *testing.B) {
	parts := make([]string, 1000)
	for i := range parts {
		parts[i] = "value" + strings.Repeat("x", i%7)
	}
	val := strings.Join(parts, ",")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := inListLiteral("mysql", val); got == "" {
			b.Fatal("empty")
		}
	}
}

// BenchmarkStripNoiseLarge：导入路径的注释/空白清洗（大 SQL 文件）。
func BenchmarkStripNoiseLarge(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("-- comment line\nINSERT INTO t VALUES (1, 'a'); /* block\ncomment */\n")
	}
	sql := sb.String()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sqlscan.StripNoise(sql)
	}
}
