package main

import (
	"strings"
	"testing"
	"time"
)

// A Redis key has no WHERE and no OFFSET: the filter dialog used to accept a
// filter on any driver, and gotoOffset's `!pageSupported() && filterSQL == ""`
// guard let a filtered key through to loadTablePage, which built
// `SELECT * FROM "key" WHERE … LIMIT 301 OFFSET 0` and handed it to the Redis
// session — which parses Redis commands, not SQL. The guard now covers both
// shapes (paging and filtering) in one predicate.
func TestRedisFilterAndPageNeverLeaveAsSQL(t *testing.T) {
	ws := newTestWorkspace()
	ws.conn = Connection{Driver: "redis"}
	sess := &fakeSession{}
	ws.sess = sess

	if ws.pageSupported() {
		t.Fatal("redis must not report SQL paging support")
	}

	tab := &dataTableTab{name: "mykey", filterSQL: "id = 1"}
	ws.activeData = tab

	// Direct calls: the full showTab pane machinery needs a rendered window,
	// which is irrelevant to the SQL-leak guard under test.
	ws.gotoOffset(1) // would previously build SELECT … OFFSET 1
	ws.applyFilter() // would previously route through gotoOffset(0)
	if len(sess.ran) != 0 {
		t.Fatalf("a Redis key must never answer a page/filter through SQL, ran: %v", sess.ran)
	}
}

// The SQL path keeps working: a paged driver with a filter still pages through
// the WHERE clause.
func TestSQLDriverFilteredPageStillQueries(t *testing.T) {
	ws := newTestWorkspace()
	ws.conn = Connection{Driver: "mysql"}
	sess := &fakeSession{}
	ws.sess = sess

	if !ws.pageSupported() {
		t.Fatal("mysql must report SQL paging support")
	}

	tab := &dataTableTab{name: "users", filterSQL: "`id` = 1"}
	tab.offset = -1 // applyFilter stamps -1 to push past gotoOffset's same-page guard
	ws.filterSQL = tab.filterSQL
	ws.activeData = tab

	ws.applyFilter()
	// loadTablePage runs the query in a safeGo worker; wait for it to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sess.ran) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	found := false
	for _, sql := range sess.ran {
		if strings.Contains(sql, "WHERE") && strings.Contains(sql, "LIMIT") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a filtered page on an SQL driver must query with WHERE+LIMIT, ran: %v", sess.ran)
	}
}
