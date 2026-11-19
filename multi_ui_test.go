package main

// 多结果 UI（§329）：一次语句返回 N 个结果集时，每个集各占一个 result tab
// （复用现有多 tab 基建），标签带「结果集 i/N」。

import (
	"testing"

	"fyne.io/fyne/v2/test"
	"fynequery/proto"
)

type multiFakeSession struct {
	fakeSession
	sets []proto.QueryResult
}

func (s *multiFakeSession) QueryMulti(string, int) ([]proto.QueryResult, error) {
	return s.sets, nil
}

func TestSendGatedMultiCreatesOneTabPerSet(t *testing.T) {
	a := test.NewApp()
	w := a.NewWindow("multi")
	session := &multiFakeSession{sets: []proto.QueryResult{
		{
			Columns: []proto.ColumnInfo{{Name: "id"}},
			Rows:    [][]proto.CellPayload{{{Text: "1"}, {Text: "2"}}}, // 1 col × 2 rows
		},
		{
			Columns: []proto.ColumnInfo{{Name: "name"}},
			Rows:    [][]proto.CellPayload{{{Text: "Alice"}}},
		},
	}}
	// 正确形状：每行的 cell 数必须等于列数。
	session.sets[0].Rows = [][]proto.CellPayload{{{Text: "1"}}, {{Text: "2"}}}
	qp := newQueryPane(w, Connection{Driver: "mysql"}, session, nil)
	qp.CreateRenderer()

	qp.applyMultiSets(session.sets, "batch", "SELECT 1; SELECT 'Alice'", 0)
	if len(qp.resultTabs) != 2 {
		t.Fatalf("tabs = %d, want 2", len(qp.resultTabs))
	}
	if qp.resultTabs[0].title != "batch · 结果集 1/2" ||
		qp.resultTabs[1].title != "batch · 结果集 2/2" {
		t.Fatalf("titles = %q / %q", qp.resultTabs[0].title, qp.resultTabs[1].title)
	}
	if len(qp.resultTabs[0].grid.data.Rows) != 2 || len(qp.resultTabs[1].grid.data.Rows) != 1 {
		t.Fatalf("rows lost: %d / %d",
			len(qp.resultTabs[0].grid.data.Rows), len(qp.resultTabs[1].grid.data.Rows))
	}
	if !qp.resultBar.Visible() {
		t.Fatal("multi results must show the tab bar")
	}
}
