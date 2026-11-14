package main

import (
	"strings"
	"testing"
)

// chartPoints 的契约测试（Q-05）：数值列抽取、NULL/非数值跳过、类目截断——
// 图表是"看趋势/离群"，不是全量展示（DFDS 原则）。
func TestChartPoints(t *testing.T) {
	data := &TableData{
		Cols: []ColumnInfo{{Name: "city", Type: "varchar"}, {Name: "amount", Type: "decimal"}},
		Rows: [][]*string{
			{str("北京"), str("120.5")},
			{str("上海"), str("98")},
			{str("广州"), nil},     // NULL → 跳过
			{str("深圳"), str("abc")}, // 非数值 → 跳过
			{nil, str("7")},        // X 为 NULL → 标签 "?"
		},
	}
	labels, values, dropped := chartPoints(data, 0, 1)
	if dropped != 2 {
		t.Errorf("应跳过 2 行（NULL/非数值），实际 %d", dropped)
	}
	if len(labels) != 3 || len(values) != 3 {
		t.Fatalf("应得 3 个点，实际 %d", len(labels))
	}
	if labels[0] != "北京" || values[0] != 120.5 {
		t.Errorf("首点错误: %s=%v", labels[0], values[0])
	}
	if labels[2] != "?" || values[2] != 7 {
		t.Errorf("X 为 NULL 的标签应为 '?': %s=%v", labels[2], values[2])
	}
}

func TestChartPointsTruncates(t *testing.T) {
	rows := make([][]*string, chartCategories+30)
	for i := range rows {
		rows[i] = []*string{str("c" + itoaTest(i)), str(itoaTest(i * 10))}
	}
	data := &TableData{
		Cols: []ColumnInfo{{Name: "k", Type: "varchar"}, {Name: "v", Type: "int"}},
		Rows: rows,
	}
	labels, _, dropped := chartPoints(data, 0, 1)
	if len(labels) != chartCategories {
		t.Errorf("应截断到 %d 类目，实际 %d", chartCategories, len(labels))
	}
	if dropped != 0 {
		t.Errorf("无跳过，实际 %d", dropped)
	}
}

func TestNumericColumnIndex(t *testing.T) {
	data := &TableData{
		Cols: []ColumnInfo{{Name: "a", Type: "varchar"}, {Name: "b", Type: "bigint"}, {Name: "c", Type: "decimal"}},
	}
	if got := numericColumnIndex(data); got != 1 {
		t.Errorf("第一个数值列应是 index 1，实际 %d", got)
	}
	allText := &TableData{Cols: []ColumnInfo{{Name: "a", Type: "text"}}}
	if got := numericColumnIndex(allText); got != -1 {
		t.Errorf("无数值列应返回 -1，实际 %d", got)
	}
}

// chartLabel：长标签截断带省略号，短标签原样。
func TestChartLabel(t *testing.T) {
	if got := chartLabel("averyveryverylonglabel", 12); !strings.HasSuffix(got, "…") {
		t.Errorf("长标签应截断带省略号: %q", got)
	}
	if got := chartLabel("ok", 12); got != "ok" {
		t.Errorf("短标签应原样: %q", got)
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
