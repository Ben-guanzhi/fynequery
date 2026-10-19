package main

// 右侧详情栏回归（P2 移植自内置版 details.go）。
// 钉住：默认隐藏 / 开关生效 / 选中行按列列出字段 / 无选中给提示 / NULL 渲染。
// 表模式的目录计数需要连接，留给 integration_test.go，这里只测纯 UI 分支。

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"tablepro-fyne/internal/db"
)

// firstLabelText 在容器树里找第一个 Label 的文本，用来断言小部件的渲染结果。
func firstLabelText(o fyne.CanvasObject) string {
	if l, ok := o.(*widget.Label); ok {
		return l.Text
	}
	if c, ok := o.(*fyne.Container); ok {
		for _, ch := range c.Objects {
			if s := firstLabelText(ch); s != "" {
				return s
			}
		}
	}
	return ""
}

func TestDetailsPaneHiddenUntilToggled(t *testing.T) {
	c := newCrumbClient(t)
	if c.details.pane == nil {
		t.Fatal("详情栏面板未建")
	}
	if c.details.pane.Visible() {
		t.Fatal("详情栏默认应隐藏（TablePlus 里也是默认收起）")
	}
	c.toggleDetails()
	if !c.details.pane.Visible() {
		t.Fatal("toggle 之后应显示")
	}
	c.toggleDetails()
	if c.details.pane.Visible() {
		t.Fatal("再 toggle 一次应重新隐藏")
	}
}

// TestDetailsRowListsOneFieldPerColumn pins the panel's main job: with a row
// selected, it lists that row's value for every column of the result.
func TestDetailsRowListsOneFieldPerColumn(t *testing.T) {
	c := newCrumbClient(t)
	c.grid.cols = []string{"id", "name", "email"}
	c.grid.colTypes = []string{"int", "varchar", "varchar"}
	c.grid.rows = [][]string{{"1", "Ann", "a@x"}}
	c.grid.hasCell = true
	c.grid.cell = widget.TableCellID{Row: 0, Col: 0}

	c.toggleDetails()
	if got := len(c.details.body.Objects); got != 3 {
		t.Fatalf("属性行数 = %d, want 3（每列一行）", got)
	}
	if got := c.details.title.Text; got == "" {
		t.Fatal("详情栏标题不应为空")
	}
}

// TestDetailsWithNoSelectionShowsHint pins the empty state: nothing selected
// and no table on screen, so the panel explains itself instead of going blank.
func TestDetailsWithNoSelectionShowsHint(t *testing.T) {
	c := newCrumbClient(t)
	c.grid.hasCell = false
	c.grid.src = db.TableRef{} // 没有打开的表 → 走空态提示分支
	c.toggleDetails()
	if got := len(c.details.body.Objects); got != 1 {
		t.Fatalf("空态属性行数 = %d, want 1（一条提示）", got)
	}
}

// TestDetailsStaticBoxRendersNull pins NULL handling: a nil value renders the
// italic NULL marker, not an empty box.
func TestDetailsStaticBoxRendersNull(t *testing.T) {
	if got := firstLabelText(staticBox(nil)); got != "NULL" {
		t.Fatalf("nil 值渲染 = %q, want %q", got, "NULL")
	}
	v := "Ann"
	if got := firstLabelText(staticBox(&v)); got != "Ann" {
		t.Fatalf("非空值渲染 = %q, want %q", got, "Ann")
	}
}

// TestIsDateTimeType covers the helper the deferred NOW() quick value keys off.
func TestIsDateTimeType(t *testing.T) {
	for _, s := range []string{"datetime", "DATE", "timestamp"} {
		if !isDateTimeType(s) {
			t.Fatalf("%q 应判定为日期时间类型", s)
		}
	}
	for _, s := range []string{"int", "varchar", ""} {
		if isDateTimeType(s) {
			t.Fatalf("%q 不应判定为日期时间类型", s)
		}
	}
}
