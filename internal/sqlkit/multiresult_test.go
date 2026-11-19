package sqlkit

// 多结果集能力（§328）：RunMultiCtx 经内核 RunMulti 读所有结果集并转成
// 客户端 proto.QueryResult。本测试验证值转换（纯函数）与驱动不返回
// 多集时的单结果路径。真正的 NextResultSet 遍历由内核的 multi 测试覆盖。

import (
	"context"
	"testing"
	"time"

	"github.com/Ben-guanzhi/dbcore"
)

func TestNativeOfAllValueKinds(t *testing.T) {
	now := time.Now()
	cases := []struct {
		v    dbcore.Value
		want interface{}
	}{
		{dbcore.Value{Kind: dbcore.ValNull}, nil},
		{dbcore.Value{Kind: dbcore.ValInt, I: 42}, int64(42)},
		{dbcore.Value{Kind: dbcore.ValFloat, F: 1.5}, 1.5},
		{dbcore.Value{Kind: dbcore.ValText, S: "hi"}, "hi"},
		{dbcore.Value{Kind: dbcore.ValDecimal, S: "123.45"}, "123.45"},
		{dbcore.Value{Kind: dbcore.ValBool, Bool: true}, true},
		{dbcore.Value{Kind: dbcore.ValTime, T: now}, now},
	}
	for _, c := range cases {
		if got := nativeOf(c.v); got != c.want {
			t.Errorf("nativeOf(%v) = %v (%T), want %v", c.v.Kind, got, got, c.want)
		}
	}
}

func TestRunMultiCtxSingleResult(t *testing.T) {
	// A statement that answers one set still comes back as exactly one grid.
	q := testDB(t)
	defer q.Close()
	sets, err := RunMultiCtx(q, context.Background(), "SELECT 1 AS id, 'Alice' AS name", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || len(sets[0].Rows) != 1 || len(sets[0].Columns) != 2 {
		t.Fatalf("single result wrong: %+v", sets)
	}
	first := sets[0].Rows[0]
	if first[0].Int != 1 || first[1].Text != "Alice" {
		t.Fatalf("cell conversion wrong: %+v", first)
	}
}
