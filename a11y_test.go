package main

// 可及性基线（S-04）：Fyne 没有 WPF 那样的 AutomationProperties，可及性的最低
// 要求同样是「每个可交互控件有可读名字」——按钮的文字就是它的名字。
//
// 规则来源：内核仓库的 dbcore/spec/a11y-baseline.json（rules[].applies_to 含 fyne）。
// 这里静态扫描本包源码：`widget.NewButton(...)` 的第一个参数必须是非空字面量或
// 提供文本的标识符。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type a11yContract struct {
	Rules []struct {
		ID        string   `json:"id"`
		AppliesTo []string `json:"applies_to"`
	} `json:"rules"`
}

func TestButtonsHaveAccessibleNames(t *testing.T) {
	path := filepath.Join("..", "dbcore", "spec", "a11y-baseline.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读可及性契约 %s: %v", path, err)
	}
	var c a11yContract
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("解析契约: %v", err)
	}
	applies := false
	for _, r := range c.Rules {
		if r.ID != "interactive-name" {
			continue
		}
		for _, s := range r.AppliesTo {
			if s == "fyne" {
				applies = true
			}
		}
	}
	if !applies {
		t.Fatal("契约的 interactive-name 必须适用于 fyne")
	}

	button := regexp.MustCompile(`widget\.NewButton\(([^,)]*)`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			m := button.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			arg := strings.TrimSpace(m[1])
			checked++
			if strings.HasPrefix(arg, `"`) {
				if strings.TrimSpace(strings.Trim(arg, `"`)) == "" {
					t.Errorf("%s:%d 按钮文字为空: %s", f, i+1, strings.TrimSpace(line))
				}
				continue
			}
			// 变量/表达式标签（含图标按钮 widget.NewButtonWithIcon 的文字参数）放行：
			// 静态检查只能证明"不是空字面量"。
		}
	}
	if checked < 30 {
		t.Fatalf("只检查了 %d 个按钮，静态扫描可能失效", checked)
	}
}
