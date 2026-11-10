package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fynequery/proto"
)

func journalTestWS(t *testing.T) *workspace {
	t.Helper()
	t.Setenv("FYNEQUERY_CONFIG_DIR", t.TempDir())
	// newTabHarness wires the single-instance chrome (dataSlot/structPane/
	// indexPane/segment/status) that showTab → applyDataTab needs; the plain
	// newTestWorkspace leaves those nil and panics on restore.
	ws := newTabHarness()
	ws.conn = Connection{Name: "dev", Driver: "mysql", Host: "h", Port: "3306", User: "u", Database: "db"}
	return ws
}

func journalEditGrid() *dataTable {
	one := func(s string) *string { return &s }
	cols := []ColumnInfo{
		{Name: "id", Type: "int", Key: "PK"},
		{Name: "name", Type: "varchar"},
	}
	rows := [][]*string{{one("1"), one("alice")}, {one("2"), one("bob")}, {one("3"), nil}}
	return newDataTable(&TableData{Cols: cols, Rows: rows, Fetched: 3, Total: 3}, nil)
}

// The full crash-safety loop: edits + queued DDL land in a journal file, a
// fresh workspace on the same connection rebuilds the tab with the edits
// re-applied (updates/deletes re-matched by PK, inserts re-staged), and the
// commit-clear path removes the file again.
func TestEditJournalRoundTrip(t *testing.T) {
	ws := journalTestWS(t)
	one := func(s string) *string { return &s }

	g := journalEditGrid()
	ws.tableName = "users"
	ws.editMode = true // the real flow turns edit mode on before the user edits
	ws.prepGrid(g)
	// saveEditJournal snapshots ws.dataTabs, so the tab must be registered the
	// way openTable does (the real flow appends the dataTableTab before load).
	ws.dataTabs = append(ws.dataTabs, &dataTableTab{name: "users", grid: g})
	ws.pendingDDL = []string{"ALTER TABLE users DROP COLUMN legacy"}

	g.setCell(0, 1, one("alice2")) // update
	g.markSelectedDeleted()        // delete: selRow defaults to 0? set explicitly below
	if g.selRow != 1 {
		g.selRow = 1
		g.deleted = map[int]bool{1: true} // mark row "bob" directly (selRow semantics vary)
		delete(g.dirty, 1)
	}
	g.stageInsert(map[string]*string{"id": one("9")}) // staged insert (name unset → NULL/default)

	// 写盘已移到后台(代际守卫串行):测试要读文件,同步 flush 出最新快照,
	// 排队的旧写入会因代数过期而放弃——结果确定。
	ws.flushEditJournal()
	path := editJournalPath(ws.conn)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("journal missing after edits: %v", err)
	}
	var j editJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatalf("journal unreadable: %v", err)
	}
	if len(j.Tabs) != 1 || j.Tabs[0].Table != "users" {
		t.Fatalf("journal tabs = %+v", j.Tabs)
	}
	tab := j.Tabs[0]
	if len(tab.Updates) != 1 || len(tab.Deletes) != 1 || len(tab.Inserts) != 1 {
		t.Fatalf("journal edits = updates:%d deletes:%d inserts:%d",
			len(tab.Updates), len(tab.Deletes), len(tab.Inserts))
	}
	if len(j.DDL) != 1 {
		t.Fatalf("journal DDL = %v", j.DDL)
	}

	// A second workspace (a relaunch) restores the journaled state.
	ws2 := journalTestWS(t)
	ws2.conn = ws.conn
	t.Setenv("FYNEQUERY_CONFIG_DIR", filepath.Dir(path)) // same store as ws
	ws2.restoreEditJournal(&j)
	if len(ws2.dataTabs) != 1 || ws2.dataTabs[0].name != "users" {
		t.Fatalf("restored tabs = %+v", ws2.dataTabs)
	}
	g2 := ws2.dataTabs[0].grid
	if g2.changeCount() == 0 {
		t.Fatal("restored grid has no pending edits")
	}
	updates, deletes, inserts := g2.pending()
	if len(updates) != 1 || updates[0].changes["name"] == nil || *updates[0].changes["name"] != "alice2" {
		t.Fatalf("restored updates = %+v", updates)
	}
	if len(deletes) != 1 || deletes[0]["id"] == nil || *deletes[0]["id"] != "2" {
		t.Fatalf("restored deletes = %+v", deletes)
	}
	if len(inserts) != 1 || inserts[0]["id"] == nil || *inserts[0]["id"] != "9" {
		t.Fatalf("restored inserts = %+v", inserts)
	}
	if _, hasName := inserts[0]["name"]; hasName {
		t.Fatalf("unset insert column must stay omitted (DB default), got %+v", inserts[0])
	}
	if len(ws2.pendingDDL) != 1 {
		t.Fatalf("restored DDL = %v", ws2.pendingDDL)
	}

	// Commit-clear: dropping the edit state rewrites the journal to nothing.
	g2.clearDirty()
	ws2.pendingDDL = nil
	ws2.flushEditJournal()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("journal must be removed when nothing is pending, stat err = %v", err)
	}
}

// Editing one connection must never touch another connection's journal, and a
// change that reverts to the original value must not leave phantom edits.
func TestEditJournalPerConnectionAndRevert(t *testing.T) {
	ws := journalTestWS(t)
	one := func(s string) *string { return &s }
	g := journalEditGrid()
	ws.prepGrid(g)

	g.setCell(0, 1, one("alice2"))
	other := ws.conn
	other.Database = "otherdb"
	if editJournalPath(ws.conn) == editJournalPath(other) {
		t.Fatal("different databases must map to different journal files")
	}
	g.setCell(0, 1, one("alice")) // back to the original value → revert
	if g.changeCount() != 0 {
		t.Fatalf("reverted edit must not stay pending, changeCount = %d", g.changeCount())
	}
	// 同步 flush 出空快照:文件必须确定性消失(排队中的旧写入按代数放弃)。
	ws.flushEditJournal()
	if _, err := os.Stat(editJournalPath(ws.conn)); !os.IsNotExist(err) {
		t.Fatalf("journal must be gone after revert, stat err = %v", err)
	}
}

// 损坏的日志文件(截断 JSON/空文件/纯垃圾)必须静默放弃恢复——不 panic、
// 不弹恢复框——且文件留待下一次保存覆盖,不阻塞新会话。
func TestEditJournalCorruptFileIgnored(t *testing.T) {
	ws := journalTestWS(t)
	path := editJournalPath(ws.conn)
	for _, content := range [][]byte{
		[]byte(`{"tabs":[`), // 截断的 JSON
		{},                  // 空文件
		[]byte("not json at all"),
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		ws.offerEditRestore() // 必须安静地返回
	}
	// 下一次保存(空状态)能覆盖损坏文件。
	ws.flushEditJournal()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("flush must overwrite/remove the corrupt journal, stat err = %v", err)
	}
}

// pageFakeSession serves loadTablePage's page query with journalEditGrid's
// rows (fakeSession's SELECT * returns no columns, which would push every
// edit into the carry and make re-match untestable). OFFSET 0 serves the full
// page; any other offset serves a different page holding only id=3 — the
// row the edits below must NOT match.
type pageFakeSession struct{ fakeSession }

func (f *pageFakeSession) Query(sql string, maxRows int) (proto.QueryResult, error) {
	if strings.HasPrefix(sql, "SELECT * FROM") {
		s := func(v string) proto.CellPayload { return proto.CellPayload{Kind: "text", Text: v} }
		cols := []proto.ColumnInfo{{Name: "id", Key: "PK"}, {Name: "name"}}
		if !strings.HasSuffix(sql, "OFFSET 0") {
			return proto.QueryResult{
				Columns: cols,
				Rows:    [][]proto.CellPayload{{s("3"), {Kind: "null"}}},
			}, nil
		}
		return proto.QueryResult{
			Columns: cols,
			Rows:    [][]proto.CellPayload{{s("1"), s("alice")}, {s("2"), s("bob")}, {s("3"), {Kind: "null"}}},
		}, nil
	}
	return f.fakeSession.Query(sql, maxRows)
}

// waitForGridSwap waits until loadTablePage's async callback has COMPLETED
// (pageDone bump = happens-before edge: grid swapped, carry re-applied,
// journal snapshotted) so the assertions below read tab state race-free.
// The test driver runs fyne.Do callbacks inline on the worker's goroutine,
// so polling plain fields would race the very callback being waited for.
func waitForGridSwap(t *testing.T, tab *dataTableTab, old *dataTable) *dataTable {
	t.Helper()
	before := tab.pageDone.Load()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tab.pageDone.Load() > before && tab.grid != old {
			return tab.grid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("loadTablePage did not replace the grid in time")
	return nil
}

// 翻页保留编辑(建议3 本体): 翻页把旧页的未保存编辑搬到新页——行在页上的
// 原位重匹配(dirty/deleted 恢复、carry 清空),行不在页上的随 tab carry 且
// 仍进日志;翻回可见页时从 carry 原位重现。回归: 旧语义整页替换连编辑一起
// 丢弃,事务条显示"干净",日志与 UI 脱节。
func TestLoadTablePageCarriesEdits(t *testing.T) {
	ws := journalTestWS(t)
	ws.setSession(&pageFakeSession{})
	one := func(s string) *string { return &s }

	g := journalEditGrid()
	ws.tableName = "users"
	ws.editMode = true
	ws.prepGrid(g)
	tab := &dataTableTab{name: "users", grid: g}
	ws.dataTabs = append(ws.dataTabs, tab)
	ws.activeData = tab

	g.setCell(0, 1, one("alice2"))    // update on id=1
	g.deleted = map[int]bool{2: true} // delete id=3
	path := editJournalPath(ws.conn)
	ws.flushEditJournal()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("journal must exist before paging: %v", err)
	}

	// 翻到 OFFSET 1(只含 id=3):id=3 的删除被新页原位收编(deleted[0]、
	// carry 消费),id=1 的更新不在页上、必须进 carry 而不是消失。
	old := tab.grid
	ws.loadTablePage(tab, 1)
	ng := waitForGridSwap(t, tab, old)
	if ng.changeCount() != 1 || !ng.deleted[0] {
		t.Fatalf("the delete's row IS on this page and must re-apply in place: count=%d deleted=%v", ng.changeCount(), ng.deleted)
	}
	if len(tab.carryUpdates) != 1 || tab.carryUpdates[0].pk["id"] == nil || *tab.carryUpdates[0].pk["id"] != "1" {
		t.Fatalf("the update's row is off-page and must ride the carry, got %+v", tab.carryUpdates)
	}
	if len(tab.carryDeletes) != 0 {
		t.Fatalf("the delete was re-hosted and must leave the carry, got %+v", tab.carryDeletes)
	}
	// 日志必须同时包含网格上的删除与 carry 里的更新。
	ws.flushEditJournal()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("journal must survive a flip whose edits are carried: %v", err)
	}
	var j editJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatalf("journal unreadable: %v", err)
	}
	if len(j.Tabs) != 1 || len(j.Tabs[0].Updates) != 1 || len(j.Tabs[0].Deletes) != 1 {
		t.Fatalf("journal must snapshot carried + on-grid edits, got %+v", j.Tabs)
	}

	// 翻回含这两行的页:carry 原位重现,carry 清空。
	old = tab.grid
	ws.loadTablePage(tab, 0)
	ng = waitForGridSwap(t, tab, old)
	if tab.carryCount() != 0 {
		t.Fatalf("carry must be consumed by a page that shows the rows, count = %d", tab.carryCount())
	}
	if ng.changeCount() == 0 {
		t.Fatal("re-applied edits must show as pending on the returned page")
	}
	// id=1 的更新回到第 0 行,id=3 的删除回到第 2 行。
	if v := ng.dirty[0][1]; v == nil || *v != "alice2" {
		t.Fatalf("carried update not re-applied in place: %+v", ng.dirty)
	}
	if !ng.deleted[2] {
		t.Fatal("carried delete not re-applied in place")
	}

	// 全部还原后,日志必须确定性消失(对账路径仍然成立)。
	ng.setCell(0, 1, one("alice"))
	ng.deleted = nil
	ng.Refresh()
	ws.flushEditJournal()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("journal must be gone once nothing is pending, stat err = %v", err)
	}
}

// carryPageEdits 纯单测: from 页的编辑进 carry;to 页装得下的原位重匹配并从
// carry 消费,装不下的留下;staged insert 永远随 carry(它不属于任何服务端页)。
func TestCarryPageEditsReappliesAndCarries(t *testing.T) {
	ws := journalTestWS(t)
	ws.tableName = "users"
	one := func(s string) *string { return &s }
	mkGrid := func(rows [][]*string) *dataTable {
		g := newDataTable(&TableData{
			Cols:    []ColumnInfo{{Name: "id", Type: "int", Key: "PK"}, {Name: "name", Type: "varchar"}},
			Rows:    rows,
			Fetched: len(rows),
			Total:   len(rows),
		}, nil)
		g.setEditable(true)
		return g
	}
	from := mkGrid([][]*string{{one("1"), one("alice")}, {one("2"), one("bob")}})
	tab := &dataTableTab{name: "users", grid: from}
	ws.dataTabs = append(ws.dataTabs, tab)
	from.setCell(0, 1, one("alice2"))                    // id=1 update
	from.stageInsert(map[string]*string{"id": one("9")}) // staged insert

	// to 页只含 id=2:id=1 的更新进 carry,id=9 的 insert 永远进 carry。
	to := mkGrid([][]*string{{one("2"), one("bob")}})
	ws.carryPageEdits(tab, from, to)
	if len(tab.carryUpdates) != 1 || *tab.carryUpdates[0].pk["id"] != "1" {
		t.Fatalf("carried updates = %+v", tab.carryUpdates)
	}
	if len(tab.carryInserts) != 1 || *tab.carryInserts[0]["id"] != "9" {
		t.Fatalf("staged inserts must always carry, got %+v", tab.carryInserts)
	}

	// 换一页装得下 id=1:更新原位重现,carry 消费;insert 继续 carry。
	back := mkGrid([][]*string{{one("1"), one("alice")}, {one("2"), one("bob")}})
	ws.carryPageEdits(tab, to, back)
	if tab.carryCount() != 1 || len(tab.carryInserts) != 1 {
		t.Fatalf("re-hosted update must leave the carry, carry = %+v/%+v/%+v",
			tab.carryUpdates, tab.carryDeletes, tab.carryInserts)
	}
	if v := back.dirty[0][1]; v == nil || *v != "alice2" {
		t.Fatalf("carried update not re-applied in place: %+v", back.dirty)
	}
}
