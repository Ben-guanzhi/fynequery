package main

// Wave 43 回归：fynequery #3（gateSafeMode 拒绝/取消后 finishRun 重置运行状态）
// 与 #4（超时重试/continueOnError 期间 running 必须保持 true）。
//
// #3 根因：gateSafeMode 的变参 cancel 回调此前不存在——Safe Mode 拒绝确认或
// 密码不匹配时 proceed 不执行，但调用方已经 armed 的 running 状态无人复位，
// 面板卡在 "Cancel" 且无法再点 Run。
//
// #4 根因：非流式 continueOnError 在错误分支调 finishRun 后继续 runChainAt，
// 但 runChainAt 内部 sendStatement 不再武装 running，重试期间面板可被再次点
// Run 并发执行。beginRun() 解决：重试/重连回调显式恢复 running。

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

// ---- #4: beginRun/finishRun 状态机 -------------------------------------------

// TestBeginRunFinishRunStateTransition 验证 running 标志与按钮标签的配对切换。
func TestBeginRunFinishRunStateTransition(t *testing.T) {
	qp := &queryPane{runBtn: newAccentButton("Run Current")}

	if qp.running {
		t.Fatal("初始状态 running 应为 false")
	}
	if qp.runBtn.label != "Run Current" {
		t.Fatalf("初始标签 = %q, want Run Current", qp.runBtn.label)
	}

	qp.beginRun()
	if !qp.running {
		t.Error("beginRun 后 running 应为 true")
	}
	if qp.runBtn.label != "Cancel" {
		t.Errorf("beginRun 后标签 = %q, want Cancel", qp.runBtn.label)
	}

	qp.finishRun()
	if qp.running {
		t.Error("finishRun 后 running 应为 false")
	}
	if qp.runBtn.label != "Run Current" {
		t.Errorf("finishRun 后标签 = %q, want Run Current", qp.runBtn.label)
	}
}

// TestBeginRunAfterFinishRestoresState 验证 finishRun 后 beginRun 能恢复运行态。
func TestBeginRunAfterFinishRestoresState(t *testing.T) {
	qp := &queryPane{runBtn: newAccentButton("Run Current")}

	qp.beginRun()
	qp.finishRun()
	qp.beginRun() // 模拟超时重试

	if !qp.running {
		t.Error("finishRun→beginRun 后 running 应恢复为 true")
	}
	if qp.runBtn.label != "Cancel" {
		t.Errorf("标签 = %q, want Cancel", qp.runBtn.label)
	}
}

// ---- #3: gateSafeMode 取消回调 -----------------------------------------------

// TestGateSafeModeUnblockedCallsProceed 验证不拦截时 proceed 立即执行、cancel 不执行。
func TestGateSafeModeUnblockedCallsProceed(t *testing.T) {
	w := test.NewWindow(nil)
	conn := Connection{SafeMode: 0} // Silent：不拦截

	proceedCalled := false
	cancelCalled := false

	gateSafeMode(w, conn, "SELECT 1", func() {
		proceedCalled = true
	}, func() {
		cancelCalled = true
	})

	if !proceedCalled {
		t.Error("Silent 模式下 proceed 应被调用")
	}
	if cancelCalled {
		t.Error("Silent 模式下 cancel 不应被调用")
	}
}

// TestGateSafeModeBlockedDoesNotCallProceed 验证拦截时 proceed 不立即执行。
func TestGateSafeModeBlockedDoesNotCallProceed(t *testing.T) {
	w := test.NewWindow(nil)
	conn := Connection{SafeMode: 1} // Alert All：全部拦截

	proceedCalled := false
	cancelCalled := false

	gateSafeMode(w, conn, "DROP TABLE t", func() {
		proceedCalled = true
	}, func() {
		cancelCalled = true
	})

	// 拦截时 confirmSafeMode 弹窗等待用户确认，proceed/cancel 都不应立即执行
	if proceedCalled {
		t.Error("拦截模式下 proceed 不应立即执行（应等用户确认）")
	}
	if cancelCalled {
		t.Error("拦截模式下 cancel 不应立即执行（应等用户拒绝）")
	}
}

// TestGateSafeModeBlockedWritesOnly 验证 Alert Write 模式只拦截写操作。
func TestGateSafeModeBlockedWritesOnly(t *testing.T) {
	w := test.NewWindow(nil)
	conn := Connection{SafeMode: 2} // Alert Write

	proceedCalled := false
	gateSafeMode(w, conn, "SELECT 1", func() {
		proceedCalled = true
	})
	if !proceedCalled {
		t.Error("Alert Write 模式下 SELECT 应放行")
	}

	proceedCalled = false
	gateSafeMode(w, conn, "DELETE FROM t", func() {
		proceedCalled = true
	})
	if proceedCalled {
		t.Error("Alert Write 模式下 DELETE 应拦截")
	}
}

// TestGateSafeModeDefaultCancelIsNoop 验证不传 cancel 回调时默认 no-op。
func TestGateSafeModeDefaultCancelIsNoop(t *testing.T) {
	w := test.NewWindow(nil)
	conn := Connection{SafeMode: 1}

	// 不传 cancel 回调：拦截时不应 panic
	gateSafeMode(w, conn, "DROP TABLE t", func() {})
}
