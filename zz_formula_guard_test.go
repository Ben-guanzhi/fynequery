package main

// CSV 公式注入防护（OWASP CSV Injection）：以 = + - @ 开头的单元格写进 CSV，
// 用户用 Excel 打开会当公式执行。必须前缀单引号；纯数字字面量豁免。

import (
	"strings"
	"testing"
)

func TestFormulaGuard(t *testing.T) {
	cases := []struct{ in, want string }{
		{"=cmd|' /C calc'!A0", "'=cmd|' /C calc'!A0"},
		{"+cmd", "'+cmd"},
		{"-cmd", "'-cmd"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"-5", "-5"},
		{"+3.14", "+3.14"},
		{"hello", "hello"},
		{"", ""},
	}
	for _, c := range cases {
		if got := formulaGuard(c.in); got != c.want {
			t.Errorf("formulaGuard(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCSVFieldGuardsFormulaInjection(t *testing.T) {
	if got := csvField("=1+1"); !strings.HasPrefix(got, `"'=1+1"`) {
		t.Errorf("危险公式没有被转义: %q", got)
	}
	if got := csvField("-5"); got != `"-5"` {
		t.Errorf("纯数字被误转义了: %q", got)
	}
}
