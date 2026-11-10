package main

// Pending-edit journal (ARIES-flavored): every edit-state change snapshots the
// workspace's unsaved grid edits — per table tab: base page rows + edits —
// plus the queued DDL to a local file. A crash or an accidental close no
// longer evaporates them; reopening the same connection offers to restore,
// and commit / rollback / closing the edited tab clears the entry.
//
// The base rows ride along because the grid's dirty map is row-index keyed:
// restoring the exact page makes index-based edits valid again without
// re-querying, and the restore still re-matches updates/deletes by primary
// key for robustness.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2/dialog"
)

type editJournalUpdate struct {
	PK      map[string]*string `json:"pk"`
	Changes map[string]*string `json:"changes"`
}

// editJournalTab snapshots one table tab: the fetched page plus its pending
// edits. Rows are the grid's base values ([]*string, nil = SQL NULL).
type editJournalTab struct {
	Table   string               `json:"table"`
	Filter  string               `json:"filter,omitempty"`
	Offset  int                  `json:"offset"`
	Total   int                  `json:"total"`
	Approx  bool                 `json:"approx,omitempty"`
	Cols    []ColumnInfo         `json:"cols"`
	Rows    [][]*string          `json:"rows"`
	Updates []editJournalUpdate  `json:"updates,omitempty"`
	Deletes []map[string]*string `json:"deletes,omitempty"`
	Inserts []map[string]*string `json:"inserts,omitempty"`
}

type editJournal struct {
	SavedAt string           `json:"savedAt"`
	Conn    string           `json:"conn"`
	DDL     []string         `json:"ddl,omitempty"`
	Tabs    []editJournalTab `json:"tabs"`
}

// journalKey names the journal file per connection: the stable part of the
// session fingerprint (engine/host/user/database — deliberately without the
// password or SSH settings, so a re-entered or keyring-filled password still
// finds its journal). Hashed, because the raw fingerprint embeds credentials
// and must never name a file.
func journalKey(conn Connection) string {
	h := fnv.New32a()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s", conn.Driver, conn.Host, conn.Port, conn.User, conn.Database)
	return fmt.Sprintf("%08x", h.Sum32())
}

func editJournalPath(conn Connection) string {
	return filepath.Join(configDir(), fmt.Sprintf("pending-edits-%s.json", journalKey(conn)))
}

// journalWriter serializes the journal's disk writes and drops superseded
// ones. saveEditJournal fires on every edit-state change — potentially per
// keystroke across many tabs — and only the LATEST snapshot must end up on
// disk: writers serialize on mu, and a writer whose generation is no longer
// current bails, so an older task can never finish after a newer one and
// overwrite it with stale content.
type journalWriter struct {
	mu  sync.Mutex
	gen uint64
}

// buildEditJournal snapshots every open table tab that has unsaved edits plus
// the queued DDL. It runs on the UI thread (it reads grid state) and stays
// cheap: the rows are copied at pointer level — no string materialization,
// which is the expensive part and moves off-thread with the marshal. The copy
// also detaches the payload from the live grid, so an off-thread marshal can
// never race an in-place row mutation (sorting, reload).
func (ws *workspace) buildEditJournal() *editJournal {
	j := &editJournal{
		SavedAt: time.Now().Format(time.RFC3339),
		Conn:    ws.conn.Name,
		DDL:     append([]string(nil), ws.pendingDDL...),
	}
	for _, t := range ws.dataTabs {
		if t == nil || t.grid == nil {
			continue
		}
		updates, deletes, inserts := t.grid.pending()
		// Carried edits (rows that scrolled off with a page flip) are part of
		// the tab's unsaved state — snapshot them too, or the journal would
		// drop exactly the edits paging just learned to preserve.
		updates = append(updates, t.carryUpdates...)
		deletes = append(deletes, t.carryDeletes...)
		inserts = append(inserts, t.carryInserts...)
		if len(updates)+len(deletes)+len(inserts) == 0 {
			continue
		}
		jupdates := make([]editJournalUpdate, 0, len(updates))
		for _, u := range updates {
			jupdates = append(jupdates, editJournalUpdate{PK: u.pk, Changes: u.changes})
		}
		rows := make([][]*string, len(t.grid.data.Rows))
		for i, r := range t.grid.data.Rows {
			rows[i] = append([]*string(nil), r...)
		}
		j.Tabs = append(j.Tabs, editJournalTab{
			Table:   t.name,
			Filter:  t.filterSQL,
			Offset:  t.offset,
			Total:   t.totalRows,
			Approx:  t.totalApprox,
			Cols:    append([]ColumnInfo(nil), t.grid.data.Cols...),
			Rows:    rows,
			Updates: jupdates,
			Deletes: deletes,
			Inserts: inserts,
		})
	}
	return j
}

// saveEditJournal snapshots the workspace's pending edits and hands the
// payload to the background writer. The old shape marshalled and wrote on the
// UI thread after every edit — fine for a tiny page, but a browse page full of
// wide TEXT values marshals to tens of MB and stalled every keystroke. The
// disk write also lands out of band now: an app exit within milliseconds of
// the LAST edit can miss it, which is the same crash window the journal
// exists for; teardown paths use flushEditJournal instead.
func (ws *workspace) saveEditJournal() {
	j := ws.buildEditJournal()
	ws.journal.mu.Lock()
	ws.journal.gen++
	gen := ws.journal.gen
	ws.journal.mu.Unlock()
	safeGo("editjournal", func() {
		ws.writeEditJournal(gen, j)
	})
}

// flushEditJournal writes the snapshot synchronously — for teardown paths
// (tab close, workspace teardown) where a debounced task might never run.
func (ws *workspace) flushEditJournal() {
	j := ws.buildEditJournal()
	ws.journal.mu.Lock()
	defer ws.journal.mu.Unlock()
	ws.journal.gen++
	ws.writeEditJournalLocked(ws.journal.gen, j)
}

// writeEditJournal is the background entry: serialized with every other write,
// and a no-op when a newer snapshot has been queued since this one was made.
func (ws *workspace) writeEditJournal(gen uint64, j *editJournal) {
	ws.journal.mu.Lock()
	defer ws.journal.mu.Unlock()
	ws.writeEditJournalLocked(gen, j)
}

// writeEditJournalLocked performs the disk work. Caller holds journal.mu.
func (ws *workspace) writeEditJournalLocked(gen uint64, j *editJournal) {
	if gen != ws.journal.gen {
		return // superseded: a newer snapshot is queued and will write instead
	}
	path := editJournalPath(ws.conn)
	if len(j.Tabs) == 0 && len(j.DDL) == 0 {
		os.Remove(path)
		return
	}
	raw, err := json.MarshalIndent(j, "", "  ")
	if err == nil {
		err = os.MkdirAll(configDir(), 0o755)
	}
	if err == nil {
		tmp := path + ".tmp"
		err = os.WriteFile(tmp, raw, 0o600)
		if err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		ws.logf("编辑日志写入失败：%v", err)
	}
}

// offerEditRestore checks for a journal left by a previous session on this
// connection and offers to bring the unsaved edits back (or drop them).
func (ws *workspace) offerEditRestore() {
	raw, err := os.ReadFile(editJournalPath(ws.conn))
	if err != nil {
		return
	}
	var j editJournal
	if err := json.Unmarshal(raw, &j); err != nil || (len(j.Tabs) == 0 && len(j.DDL) == 0) {
		return
	}
	var parts []string
	for _, tab := range j.Tabs {
		parts = append(parts, fmt.Sprintf("%s：%d 处修改、%d 处删除、%d 行新增",
			tab.Table, len(tab.Updates), len(tab.Deletes), len(tab.Inserts)))
	}
	if len(j.DDL) > 0 {
		parts = append(parts, fmt.Sprintf("%d 条待执行 DDL", len(j.DDL)))
	}
	dialog.NewConfirm("恢复未提交编辑",
		"上次会话在此连接上有未提交的编辑：\n"+strings.Join(parts, "\n")+
			"\n\n恢复到当前工作区吗？（丢弃将删除该记录）",
		func(ok bool) {
			if ok {
				ws.restoreEditJournal(&j)
			} else {
				os.Remove(editJournalPath(ws.conn))
			}
		}, ws.window).Show()
}

// restoreEditJournal rebuilds one table tab per snapshot from the journaled
// page + edits (no re-query) and activates the last one.
func (ws *workspace) restoreEditJournal(j *editJournal) {
	ws.pendingDDL = append([]string(nil), j.DDL...)
	last := -1
	for i := range j.Tabs {
		tab := &j.Tabs[i]
		t := &dataTableTab{
			name: tab.Table, filterSQL: tab.Filter, offset: tab.Offset,
			totalRows: tab.Total, totalApprox: tab.Approx, loaded: true,
		}
		t.cols = append([]ColumnInfo(nil), tab.Cols...)
		data := &TableData{
			Cols:    append([]ColumnInfo(nil), tab.Cols...),
			Rows:    tab.Rows,
			Fetched: len(tab.Rows),
			Total:   tab.Total,
			Approx:  tab.Approx,
		}
		grid := newDataTable(data, ws.status)
		ws.prepGrid(grid)
		// A restarted workspace has edit mode off, but a journaled tab carries
		// unsaved updates/deletes that commit re-matches by primary key. The
		// grid must be editable for pkCols to be set, and pending() skips any
		// row without them — so without this the restored edits would be shown
		// as pending yet silently dropped by Commit.
		grid.setEditable(true)
		grid.restorePending(tab.Updates, tab.Deletes, tab.Inserts)
		t.grid = grid
		ws.dataTabs = append(ws.dataTabs, t)
		last = ws.addDoc(docData, tab.Table, true, t, nil)
	}
	if last >= 0 {
		ws.tabs.setActive(last)
		ws.showTab(last) // applyDataTab pushes the grid and re-syncs the chrome
	}
	ws.syncTabStrip()
	ws.updateBanner()
	ws.saveEditJournal()
	ws.logf("已恢复 %d 个表的未提交编辑", len(j.Tabs))
}

// ---- grid-side restore -----------------------------------------------------

// findRowByPK returns the index of the row whose primary-key cells match pk
// (-1 when none). The scan is tiny (one browse page) and runs once per edit.
func (t *dataTable) findRowByPK(pk map[string]*string) int {
	if len(pk) == 0 {
		return -1
	}
	for row, r := range t.data.Rows {
		match := true
		for name, v := range pk {
			ci := -1
			for i, c := range t.data.Cols {
				if c.Name == name {
					ci = i
					break
				}
			}
			if ci < 0 || ci >= len(r) || !ptrEqual(r[ci], v) {
				match = false
				break
			}
		}
		if match {
			return row
		}
	}
	return -1
}

// restorePending re-applies a journal snapshot's edits onto this grid.
// Updates and deletes re-match by primary key; inserts are re-staged rows.
func (t *dataTable) restorePending(updates []editJournalUpdate, deletes, inserts []map[string]*string) {
	for _, u := range updates {
		row := t.findRowByPK(u.PK)
		if row < 0 || t.deleted[row] {
			continue
		}
		for name, v := range u.Changes {
			for ci, c := range t.data.Cols {
				if c.Name == name {
					if t.dirty == nil {
						t.dirty = map[int]map[int]*string{}
					}
					if t.dirty[row] == nil {
						t.dirty[row] = map[int]*string{}
					}
					t.dirty[row][ci] = v
					break
				}
			}
		}
	}
	for _, pk := range deletes {
		if row := t.findRowByPK(pk); row >= 0 {
			if t.deleted == nil {
				t.deleted = map[int]bool{}
			}
			t.deleted[row] = true
			delete(t.dirty, row) // an edited-then-deleted row is simply deleted
		}
	}
	for _, ins := range inserts {
		t.stageInsert(ins)
	}
	t.Refresh()
}
