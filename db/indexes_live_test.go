package db

import (
	"net/url"
	"os"
	"testing"

	"fynequery/proto"
)

// Live smoke test for the kernel-backed Indexes page: it connects with the real
// MySQL dialer, creates a table with a composite index, and reads the metadata
// back through IndexLister (dbcore.SQLDriver.ListIndexes).
//
// Skipped unless FYNEQUERY_LIVE_MYSQL is set, e.g.
//
//	FYNEQUERY_LIVE_MYSQL=mysql://root:dbcore@127.0.0.1:53306/dbcore
func TestLiveMySQLIndexes(t *testing.T) {
	raw := os.Getenv("FYNEQUERY_LIVE_MYSQL")
	if raw == "" {
		t.Skip("FYNEQUERY_LIVE_MYSQL not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	password, _ := u.User.Password()
	params := proto.ConnectParams{
		Name:     "live",
		Host:     u.Hostname(),
		Port:     u.Port(),
		User:     u.User.Username(),
		Password: password,
		Database: u.Path[1:],
	}
	dialer, ok := Driver("mysql")
	if !ok {
		t.Fatal("mysql driver not registered")
	}
	sess, err := dialer.Connect(params)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	if _, err := sess.Query("DROP TABLE IF EXISTS dbcore_live_idx", 1); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := sess.Query("CREATE TABLE dbcore_live_idx (id INT PRIMARY KEY, a VARCHAR(16), b VARCHAR(16), KEY idx_ab (a, b))", 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Query("DROP TABLE IF EXISTS dbcore_live_idx", 1)

	lister, ok := sess.(IndexLister)
	if !ok {
		t.Fatal("mysql session must implement IndexLister")
	}
	infos, err := lister.ListIndexes(params.Database, "dbcore_live_idx")
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	var primary, composite *struct {
		columns []string
		unique  bool
	}
	for i := range infos {
		switch infos[i].Name {
		case "PRIMARY":
			primary = &struct {
				columns []string
				unique  bool
			}{infos[i].Columns, infos[i].Unique}
		case "idx_ab":
			composite = &struct {
				columns []string
				unique  bool
			}{infos[i].Columns, infos[i].Unique}
		}
	}
	if primary == nil || len(primary.columns) != 1 || primary.columns[0] != "id" || !primary.unique {
		t.Fatalf("PRIMARY index metadata wrong: %+v (all: %+v)", primary, infos)
	}
	if composite == nil || len(composite.columns) != 2 || composite.columns[0] != "a" || composite.columns[1] != "b" || composite.unique {
		t.Fatalf("composite index metadata wrong: %+v (all: %+v)", composite, infos)
	}
}
