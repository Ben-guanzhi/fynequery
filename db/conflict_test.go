package db

// 并发写冲突故障注入（§333）：OCC 的客户端实践——keyed UPDATE 影响
// 0 行 = 另一个写者已改/已删该行，applyWithin 必须报 ErrRowMismatch，
// 且**整批未生效**（先改后验、冲突整批回滚的语义）。

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func conflictFixture(t *testing.T) (*sql.Conn, *sql.Conn, *sql.Tx) {
	t.Helper()
	// 文件型库：两个独立连接 = 真正的两个写者（:memory: 每连接独立库，
	// 同一连接上的"另一个写者"不成立——它看到的是自己事务里的数据）。
	db, err := sql.Open("sqlite", t.TempDir()+"/conflict.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	setup := func() *sql.Conn {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	a := setup()
	if _, err := a.ExecContext(context.Background(),
		"CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecContext(context.Background(),
		"INSERT INTO items VALUES (1, 'original', 5)"); err != nil {
		t.Fatal(err)
	}
	// 提交方的连接 b：事务由调用方在合适的时机打开（窗口顺序是测试语义
	// 的一部分——SQLite 的写锁让挂起事务与其它连接的写入互等）。
	b := setup()
	return a, b, nil
}

func TestApplyWithinDetectsConcurrentUpdate(t *testing.T) {
	other, mine, _ := conflictFixture(t)
	tx, err := mine.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// 另一个写者抢先改了这一行（在提交方读取之后、写回之前）。
	if _, err := other.ExecContext(context.Background(), "UPDATE items SET name = 'stolen' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	err = applyWithin(tx, "items", []RowEdit{
		{
			PK:      map[string]*string{"id": strPtr("1")},
			Changes: map[string]*string{"name": strPtr("mine")},
			// 读时基准：name 原值是 "original"。另一写者已把它改成
			// "stolen"——乐观谓词不匹配，必须报冲突而不是静默覆盖。
			Expect: map[string]*string{"name": strPtr("original")},
		},
	}, nil, nil, nil)
	if !errors.Is(err, ErrRowMismatch) {
		t.Fatalf("want ErrRowMismatch, got %v", err)
	}

	// 整批未生效：externally-visible 的行还是抢先写者的值。
	_ = tx.Rollback()
	var name string
	if err := other.QueryRowContext(context.Background(), "SELECT name FROM items WHERE id = 1").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "stolen" {
		t.Fatalf("concurrent writer's value lost: %q", name)
	}
}

func TestApplyWithinDetectsConcurrentDelete(t *testing.T) {
	other, mine, _ := conflictFixture(t)

	// 另一个写者删了这一行——keyed DELETE 影响 0 行同样必须报冲突。
	// 提交方的事务在删除**之后**打开：SQLite 的写锁会让挂起事务与
	// 其它连接的写入互等，这里先让删除落地再开 tx（冲突的判定是
	// "行不存在"本身，与窗口顺序无关）。
	if _, err := other.ExecContext(context.Background(), "DELETE FROM items WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	tx, err := mine.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	err = applyWithin(tx, "items", nil,
		[]map[string]*string{{"id": strPtr("1")}}, nil, nil)
	if !errors.Is(err, ErrRowMismatch) {
		tx.Rollback() // 让 cleanup 能归还连接（不吞断言失败）
		t.Fatalf("want ErrRowMismatch for vanished row, got %v", err)
	}
	_ = tx.Rollback()
}

func TestApplyWithinHappyPath(t *testing.T) {
	_, mine, _ := conflictFixture(t)
	tx, err := mine.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = applyWithin(tx, "items", []RowEdit{
		{PK: map[string]*string{"id": strPtr("1")}, Changes: map[string]*string{"qty": strPtr("9")}},
	}, nil, []map[string]*string{{"id": strPtr("2"), "name": strPtr("new"), "qty": strPtr("1")}},
		[]string{"CREATE INDEX idx_qty ON items(qty)"}) // DDL 豁免行数校验
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var qty int
	if err := mine.QueryRowContext(context.Background(), "SELECT qty FROM items WHERE id = 1").Scan(&qty); err != nil || qty != 9 {
		t.Fatalf("update lost: qty=%d err=%v", qty, err)
	}
	var n int
	if err := mine.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM items").Scan(&n); err != nil || n != 2 {
		t.Fatalf("insert lost: n=%d err=%v", n, err)
	}
}
