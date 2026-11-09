package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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

	g.setCell(0, 1, one("alice2"))                    // update
	g.markSelectedDeleted()                            // delete: selRow defaults to 0? set explicitly below
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

// 翻页×日志对账回归(P2 修复):翻页整页替换网格、旧页编辑随之丢弃——显式
// 导航与关页签同语义。修复前翻页路径不触发日志对账,磁盘快照继续宣称"该表
// 有未提交编辑",下次启动会弹出恢复已放弃编辑的提示。
func TestLoadTablePageReconcilesJournal(t *testing.T) {
	ws := journalTestWS(t)
	ws.setSession(&fakeSession{})
	one := func(s string) *string { return &s }

	g := journalEditGrid()
	ws.tableName = "users"
	ws.editMode = true
	ws.prepGrid(g)
	tab := &dataTableTab{name: "users", grid: g}
	ws.dataTabs = append(ws.dataTabs, tab)

	g.setCell(0, 1, one("alice2"))
	ws.flushEditJournal()
	path := editJournalPath(ws.conn)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("journal must exist before paging: %v", err)
	}

	old := tab.grid
	ws.loadTablePage(tab, 0)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && tab.grid == old {
		time.Sleep(10 * time.Millisecond)
	}
	if tab.grid == old {
		t.Fatal("loadTablePage did not replace the grid in time")
	}
	// 对账保存(gen 已更新)必须让文件确定性消失;排队中的旧写入按代数放弃。
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("journal still claims edits for a page the session discarded")
}
