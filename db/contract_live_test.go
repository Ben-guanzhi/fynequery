package db

import (
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
	dbcoremysql "github.com/dbcore/dbcore/mysql"

	"fynequery/proto"
)

// TestLiveEngineContract runs the shared engine contract (dbcore/drivertest)
// through fynequery's own session glue: the real MySQL dialer opens the
// connection, and the contract then exercises the wrapped *sql.DB.
//
// fynequery keeps its own Session/DML layer, so this is the guard that its
// connection handling stays compatible with the kernel's expectations.
//
// Skipped unless FYNEQUERY_LIVE_MYSQL is set.
func TestLiveEngineContract(t *testing.T) {
	raw := os.Getenv("FYNEQUERY_LIVE_MYSQL")
	if raw == "" {
		t.Skip("FYNEQUERY_LIVE_MYSQL not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	password, _ := u.User.Password()
	database := u.Path[1:]
	params := proto.ConnectParams{
		Name: "contract", Host: u.Hostname(), Port: u.Port(),
		User: u.User.Username(), Password: password, Database: database,
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

	sqlSess, ok := sess.(*mysqlSession)
	if !ok {
		t.Fatalf("unexpected session type %T", sess)
	}
	drivertest.Run(t, drivertest.Spec{
		Kind:   dbcore.KindMySQL,
		Schema: database,
		Open: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
			return dbcoremysql.NewFromDB(sqlSess.db), nil
		},
	})
}
