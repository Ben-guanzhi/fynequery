package db

// 事务悬挂故障注入（§330）：10 分钟定时器的 rollback 路径没有测试覆盖。
// 这里直接调 expireTx（绕过真实定时器），验证三个关键不变量：
//  1. 过期后 tx/txConn/txTimer 清空、txExpired=true;
//  2. 随后的 Commit/Rollback 报"expired and was rolled back"而不是
//     "no active transaction"（用户知道发生了什么）;
//  3. 迟到的定时器不能误伤已替换的新事务（identity check）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestExpireTxRollsBackAndReports(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// A pinned *sql.Conn is what a real Begin holds; nil is accepted by expireTx
	// too, but exercise the real shape.
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := &mysqlSession{
		tx:      tx,
		txConn:  conn,
		txTimer: time.AfterFunc(time.Hour, func() {}),
	}
	s.expireTx(tx)

	s.mu.Lock()
	cleared := s.tx == nil && s.txConn == nil && s.txTimer == nil && s.txExpired
	s.mu.Unlock()
	if !cleared {
		t.Fatal("expireTx did not clear the transaction state")
	}
	if err := s.Commit(); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Commit after expiry = %v, want an expired error", err)
	}
	// The rolled-back tx is unusable (proof the rollback really happened).
	if err := tx.Commit(); err == nil {
		t.Fatal("expired transaction should no longer commit")
	}
}

func TestExpireTxLateFireDoesNotTouchNewTx(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	oldTx, _ := db.BeginTx(context.Background(), nil)
	newTx, _ := db.BeginTx(context.Background(), nil)
	s := &mysqlSession{tx: newTx}

	// A stale timer for the OLD transaction fires after a new one is active.
	s.expireTx(oldTx)

	s.mu.Lock()
	stillNew := s.tx == newTx && !s.txExpired
	s.mu.Unlock()
	if !stillNew {
		t.Fatal("late expireTx touched the replacement transaction")
	}
	if err := newTx.Rollback(); err != nil {
		t.Fatalf("new transaction should still be usable: %v", err)
	}
}
