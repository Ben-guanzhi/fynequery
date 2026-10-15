package db

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Contract tests dial a real server. Everything else in this package proves the
// dialect table is complete and renders the SQL it claims to; only these prove
// that the catalog SQL those dialects emit is answered the way the client reads
// it back - which column a scanner expects, which name a catalog reports, whether
// an OFFSET window comes back disjoint, and whether a write the grid queues is
// accepted, counted and rolled back the way the client promises. A unit test
// cannot fake any of that.
//
// They are opt-in and loud about it: set FYNE_CONTRACT_MYSQL_HOST (and the rest
// of that engine's vars) or FYNE_CONTRACT_PG_HOST to run them. Set
// FYNE_CONTRACT_REQUIRED to a comma list of engines (mysql, postgres) and a
// missing target fails the run instead of skipping it - that is what a CI job
// with service containers uses, so a typo in a service variable cannot turn the
// contract back into silent green. FYNE_CONTRACT_WAIT_SECONDS lets that same
// job wait out a container that is still coming up; without it a refused dial
// is reported immediately.

const (
	contractRows     = 25
	contractPageSize = 10
)

// contractTarget is one engine to dial plus where its fixture lives.
type contractTarget struct {
	engine   string // DbTypeMySQL or DbTypePostgres
	envPfx   string // FYNE_CONTRACT_MYSQL / FYNE_CONTRACT_PG
	host     string
	port     int
	user     string
	pass     string
	database string
	// schema is what the catalog reads are pointed at: MySQL resolves a schema
	// to the database itself, PostgreSQL keeps public separate from it.
	schema string
}

// contractEngines is the roster a contract run can ask for. The env prefix lives
// here rather than being derived from the engine name because DbTypePostgres
// reads "postgresql" while the variable is FYNE_CONTRACT_PG - and a skip message
// that names a variable nobody can set is no use at all.
var contractEngines = []contractTarget{
	{engine: DbTypeMySQL, envPfx: "FYNE_CONTRACT_MYSQL", database: "fyne_contract"},
	{engine: DbTypePostgres, envPfx: "FYNE_CONTRACT_PG", database: "fyne_contract", schema: "public"},
}

// contractTargets reads the environment; nothing here is required to exist, and
// requireContractTarget is what decides between a skip and a failure.
func contractTargets() []contractTarget {
	var out []contractTarget
	for _, cand := range contractEngines {
		host := os.Getenv(cand.envPfx + "_HOST")
		if host == "" {
			continue
		}
		port, _ := strconv.Atoi(os.Getenv(cand.envPfx + "_PORT"))
		if port == 0 {
			port = 3306
			if cand.engine == DbTypePostgres {
				port = 5432
			}
		}
		cand.host = host
		cand.port = port
		cand.user = os.Getenv(cand.envPfx + "_USER")
		cand.pass = os.Getenv(cand.envPfx + "_PASSWORD")
		if db := os.Getenv(cand.envPfx + "_DATABASE"); db != "" {
			cand.database = db
		}
		if sc := os.Getenv(cand.envPfx + "_SCHEMA"); sc != "" {
			cand.schema = sc
		}
		out = append(out, cand)
	}
	return out
}

// newContractStore dials one target and hands back a Store connected to it with
// the fixture recreated. It fails (never skips) once a target was chosen: an
// unreachable server is a broken contract run, not a reason to wave it through.
func newContractStore(t *testing.T, target contractTarget) *Store {
	t.Helper()
	// The fixture must not read or write anyone's saved connections or history.
	t.Setenv("TABLEPRO_STATE_DIR", t.TempDir())

	store := NewStore()
	t.Cleanup(store.CloseAll)

	cfg := ConnConfig{
		ID: "contract", Name: "contract", Type: target.engine,
		Host: target.host, Port: target.port,
		Username: target.user, Password: target.pass,
		SSLMode: "false",
	}
	// The fixture database has to exist before it can be the one connected to,
	// so each target is dialled twice: once against the server's own baseline
	// database to create it, then against the fixture. Replacing the live entry
	// with a second Connect is what changes the target database - an open Conn's
	// Config is read without a lock, so it is never edited in place.
	cfg.Database = contractBootstrap(target.engine)
	conn := contractConnect(t, store, cfg)
	ensureContractDatabase(t, store, conn, target.database)
	cfg.Database = target.database
	conn = contractConnect(t, store, cfg)

	schema := ResolveSchema(target.engine, target.database, target.schema)
	runContractStmts(t, store, conn, fixtureTeardown(target.engine, schema))
	runContractStmts(t, store, conn, fixtureSchema(target.engine, schema))
	runContractStmts(t, store, conn, fixtureData(target.engine, schema))
	t.Cleanup(func() {
		c, err := store.OpenConn("contract")
		if err != nil {
			return
		}
		for _, stmt := range fixtureTeardown(target.engine, schema) {
			// t.Fatalf is off-limits once the test has spoken: a stubborn
			// leftover is a log line, not a second failure.
			if res := store.Execute(context.Background(), c, target.database, stmt, 0); res.Error != "" {
				t.Logf("fixture teardown: %s: %s", firstWords(stmt, 6), res.Error)
			}
		}
	})
	return store
}

// contractBootstrap names a database every server answers on before the fixture
// exists: MySQL needs none, PostgreSQL will not accept an empty dbname.
func contractBootstrap(engine string) string {
	if engine == DbTypePostgres {
		return "postgres"
	}
	return ""
}

// contractConnect replaces the live entry with this config and returns the open
// connection, reporting the database it tried to land on.
func contractConnect(t *testing.T, store *Store, cfg ConnConfig) *Conn {
	t.Helper()
	deadline := time.Now().Add(contractReadyWindow())
	for {
		err := store.Connect(cfg)
		if err == nil {
			conn, openErr := store.OpenConn(cfg.ID)
			if openErr != nil {
				t.Fatalf("open conn: %v", openErr)
			}
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect %s at %s:%d (database %q, dial window %v): %v",
				cfg.Type, cfg.Host, cfg.Port, cfg.Database, contractReadyWindow(), err)
		}
		t.Logf("%s at %s:%d not accepting yet: %v", cfg.Type, cfg.Host, cfg.Port, err)
		time.Sleep(2 * time.Second)
	}
}

// contractReadyWindow is how long a configured target may keep refusing before
// the run calls it broken. GitHub's service containers can pass their health
// check while the image is still running the temporary, networking-disabled
// server it uses to initialize, so a CI job opts into waiting that out with
// FYNE_CONTRACT_WAIT_SECONDS; a hand-typed host on a laptop is reported at once
// instead of hanging for a minute and a half.
func contractReadyWindow() time.Duration {
	seconds, _ := strconv.Atoi(os.Getenv("FYNE_CONTRACT_WAIT_SECONDS"))
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// ensureContractDatabase creates the fixture database when the server does not
// already have it. PostgreSQL has no IF NOT EXISTS for CREATE DATABASE, so a
// duplicate is recognised from the error the server raises.
func ensureContractDatabase(t *testing.T, store *Store, conn *Conn, database string) {
	t.Helper()
	stmt := "CREATE DATABASE IF NOT EXISTS " + contractIdent(conn.Config.Type, database)
	if conn.Config.Type == DbTypePostgres {
		stmt = "CREATE DATABASE " + contractIdent(conn.Config.Type, database)
	}
	res := store.Execute(context.Background(), conn, conn.Config.Database, stmt, 0)
	if res.Error != "" && !strings.Contains(strings.ToLower(res.Error), "already exists") {
		t.Fatalf("create fixture database %q: %s", database, res.Error)
	}
}

// requireContractTarget resolves one engine's fixture for a subtest, skipping
// only when the run was never asked for that engine.
func requireContractTarget(t *testing.T, engine string) (*Store, contractTarget) {
	t.Helper()
	pfx := contractEnvPrefix(engine)
	for _, cand := range contractTargets() {
		if cand.engine == engine {
			cand.schema = ResolveSchema(engine, cand.database, cand.schema)
			return newContractStore(t, cand), cand
		}
	}
	if contractRequired(engine) {
		t.Fatalf("%s contract requested by FYNE_CONTRACT_REQUIRED but no %s_HOST is configured", engine, pfx)
	}
	t.Skipf("%s contract skipped: set %s_HOST (and _PORT/_USER/_PASSWORD) to dial a real server", engine, pfx)
	return nil, contractTarget{}
}

// contractEnvPrefix answers the variable family one engine is configured under,
// so a skip or a required-run failure names a variable the reader can set.
func contractEnvPrefix(engine string) string {
	for _, cand := range contractEngines {
		if cand.engine == engine {
			return cand.envPfx
		}
	}
	return "FYNE_CONTRACT_" + strings.ToUpper(engine)
}

// contractRequired reports whether the run demanded this engine. FYNE_CONTRACT_
// REQUIRED is written by a person into a CI YAML file, so the aliases a user
// reaches for ("postgres", "pg") resolve to the engine identifier the roster uses.
func contractRequired(engine string) bool {
	for _, want := range strings.Split(os.Getenv("FYNE_CONTRACT_REQUIRED"), ",") {
		if contractEngineAlias(strings.ToLower(strings.TrimSpace(want))) == engine {
			return true
		}
	}
	return false
}

func contractEngineAlias(token string) string {
	switch token {
	case "pg", "postgres":
		return DbTypePostgres
	case "mariadb":
		return DbTypeMySQL
	}
	return token
}

// runContractStmts runs each statement in order and fails on the first error,
// naming the statement rather than leaving a fixture half-built.
func runContractStmts(t *testing.T, store *Store, conn *Conn, stmts []string) {
	t.Helper()
	for i, stmt := range stmts {
		if res := store.Execute(context.Background(), conn, conn.Config.Database, stmt, 0); res.Error != "" {
			t.Fatalf("fixture statement %d (%s): %s", i+1, firstWords(stmt, 8), res.Error)
		}
	}
}

func firstWords(s string, n int) string {
	fields := strings.Fields(s)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

// contractIdent quotes a fixture name with the engine's own identifier rules, so
// the fixture travels through the same quoting the client uses. Only MySQL and
// PostgreSQL have a contract, and quoteIdent cannot fail for them; if it ever
// does, the raw name surfaces as a broken statement instead of a silent skip.
func contractIdent(engine, name string) string {
	quoted, err := quoteIdent(engine, name)
	if err != nil {
		return name
	}
	return quoted
}

func contractObject(engine, schema, table string) string {
	return contractIdent(engine, schema) + "." + contractIdent(engine, table)
}

// The fixture is a parent/child pair with a real foreign key, a non-unique index
// and a view - the shapes every catalog read in dialect.go is written against.
// The keys are plain NOT NULL columns rather than AUTO_INCREMENT/SERIAL on
// purpose: an identity column makes the catalog report a nextval() default, and
// the DDL the structure page rebuilds would then name a sequence that a clone
// table does not have. The contract under test is the catalog read, not the
// server's identity syntax, so the fixture keeps defaults out of it.
func fixtureSchema(engine, schema string) []string {
	parent := contractObject(engine, schema, "tpro_parent")
	child := contractObject(engine, schema, "tpro_child")
	if engine == DbTypeMySQL {
		return []string{
			"CREATE TABLE " + parent + " (id INT NOT NULL PRIMARY KEY, name VARCHAR(64) NOT NULL)",
			"CREATE TABLE " + child + " (id INT NOT NULL PRIMARY KEY, parent_id INT NOT NULL, " +
				"score DECIMAL(10,2) NULL, CONSTRAINT fk_tpro_child_parent FOREIGN KEY (parent_id) " +
				"REFERENCES " + parent + " (id))",
			"CREATE INDEX idx_tpro_child_parent ON " + child + " (parent_id)",
			"CREATE VIEW " + contractObject(engine, schema, "tpro_child_view") +
				" AS SELECT id, parent_id FROM " + child,
		}
	}
	return []string{
		"CREATE TABLE " + parent + " (id BIGINT NOT NULL PRIMARY KEY, name TEXT NOT NULL)",
		"CREATE TABLE " + child + " (id BIGINT NOT NULL PRIMARY KEY, parent_id BIGINT NOT NULL, " +
			"score NUMERIC(10,2), CONSTRAINT fk_tpro_child_parent FOREIGN KEY (parent_id) " +
			"REFERENCES " + parent + " (id))",
		"CREATE INDEX idx_tpro_child_parent ON " + child + " (parent_id)",
		"CREATE VIEW " + contractObject(engine, schema, "tpro_child_view") +
			" AS SELECT id, parent_id FROM " + child,
	}
}

func fixtureTeardown(engine, schema string) []string {
	// View first: a view is not a table, and MySQL refuses to drop one with
	// DROP TABLE. Child before parent because of the foreign key - which is also
	// why the order, not a CASCADE clause, does the work for MySQL: its DROP
	// TABLE has no CASCADE option, only PostgreSQL's does.
	names := []struct {
		object string
		isView bool
	}{
		{contractObject(engine, schema, "tpro_child_view"), true},
		{contractObject(engine, schema, "tpro_child"), false},
		{contractObject(engine, schema, "tpro_parent"), false},
	}
	suffix := ""
	if engine == DbTypePostgres {
		suffix = " CASCADE"
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		verb := "DROP TABLE IF EXISTS "
		if n.isView {
			verb = "DROP VIEW IF EXISTS "
		}
		out = append(out, verb+n.object+suffix)
	}
	return out
}

// fixtureData seeds contractRows parents and a child on each of the first five,
// leaving the remaining scores NULL so the grid's NULL rendering has a row to
// prove it on.
func fixtureData(engine, schema string) []string {
	parent := contractObject(engine, schema, "tpro_parent")
	child := contractObject(engine, schema, "tpro_child")
	out := make([]string, 0, contractRows*2)
	for i := 1; i <= contractRows; i++ {
		out = append(out, fmt.Sprintf("INSERT INTO %s (id, name) VALUES (%d, 'parent-%d')", parent, i, i))
		if i <= 5 {
			out = append(out, fmt.Sprintf("INSERT INTO %s (id, parent_id, score) VALUES (%d, %d, %d.50)",
				child, i, i, i))
		}
	}
	return out
}

func TestContractMySQLCatalog(t *testing.T)    { testCatalogContract(t, DbTypeMySQL) }
func TestContractPostgresCatalog(t *testing.T) { testCatalogContract(t, DbTypePostgres) }

// testCatalogContract is the contract every dialect row promises: the catalog
// reads return the fixture, name it the way the client addresses it, and give
// back the primary key, foreign key and index the DDL declared.
func testCatalogContract(t *testing.T, engine string) {
	store, target := requireContractTarget(t, engine)
	conn, err := store.OpenConn("contract")
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	dbName, schema := target.database, target.schema

	t.Run("databases list the fixture", func(t *testing.T) {
		dbs, err := store.Databases("contract")
		if err != nil {
			t.Fatalf("Databases: %v", err)
		}
		if !contains(dbs, dbName) {
			t.Fatalf("Databases() = %v, want it to contain %q", dbs, dbName)
		}
	})

	t.Run("tables list the fixture with types", func(t *testing.T) {
		tables, err := store.Tables("contract", dbName, schema)
		if err != nil {
			t.Fatalf("Tables: %v", err)
		}
		got := map[string]string{}
		for _, tbl := range tables {
			got[tbl.Name] = tbl.Type
		}
		for name, want := range map[string]string{
			"tpro_parent": "table", "tpro_child": "table", "tpro_child_view": "view",
		} {
			if got[name] != want {
				t.Errorf("table %q type = %q, want %q (%d rows listed)", name, got[name], want, len(tables))
			}
		}
	})

	t.Run("columns carry the primary key and nullability", func(t *testing.T) {
		cols, err := store.Columns("contract", dbName, schema, "tpro_child")
		if err != nil {
			t.Fatalf("Columns: %v", err)
		}
		byName := map[string]ColumnInfo{}
		for _, col := range cols {
			byName[strings.ToLower(col.Name)] = col
		}
		id, ok := byName["id"]
		if !ok {
			t.Fatalf("Columns() = %+v, want an id column", cols)
		}
		if !id.IsPrimaryKey {
			t.Errorf("id.IsPrimaryKey = false, want the declared primary key found")
		}
		if id.Nullable {
			t.Errorf("id.Nullable = true for a NOT NULL key column")
		}
		parent, ok := byName["parent_id"]
		if !ok {
			t.Fatalf("Columns() = %+v, want a parent_id column", cols)
		}
		if parent.IsPrimaryKey {
			t.Errorf("parent_id.IsPrimaryKey = true, only id is the key")
		}
		score, ok := byName["score"]
		if !ok {
			t.Fatalf("Columns() = %+v, want a score column", cols)
		}
		if !score.Nullable {
			t.Errorf("score.Nullable = false, the fixture declares it nullable")
		}
		// An empty type would leave the grid unable to decide between a number
		// and a string, which is what makes a quoted literal out of an int.
		for name, col := range byName {
			if strings.TrimSpace(col.Type) == "" {
				t.Errorf("column %q reports an empty type", name)
			}
		}
	})

	t.Run("foreign keys resolve to the parent table", func(t *testing.T) {
		fks, err := store.ForeignKeys("contract", dbName, schema, "tpro_child")
		if err != nil {
			t.Fatalf("ForeignKeys: %v", err)
		}
		if len(fks) != 1 {
			t.Fatalf("ForeignKeys() = %+v, want exactly the one declared constraint", fks)
		}
		fk := fks[0]
		if !strings.EqualFold(fk.RefTable, "tpro_parent") {
			t.Errorf("RefTable = %q, want tpro_parent", fk.RefTable)
		}
		if len(fk.RefColumns) != 1 || !strings.EqualFold(fk.RefColumns[0], "id") {
			t.Errorf("RefColumns = %v, want [id]", fk.RefColumns)
		}
		if len(fk.Columns) != 1 || !strings.EqualFold(fk.Columns[0], "parent_id") {
			t.Errorf("Columns = %v, want [parent_id]", fk.Columns)
		}
	})

	t.Run("indexes include the declared non-unique one", func(t *testing.T) {
		idxs, err := store.TableIndexes("contract", dbName, schema, "tpro_child")
		if err != nil {
			t.Fatalf("TableIndexes: %v", err)
		}
		var declared, primary *IndexInfo
		for i := range idxs {
			switch {
			case strings.EqualFold(idxs[i].Name, "idx_tpro_child_parent"):
				declared = &idxs[i]
			case idxs[i].Primary:
				primary = &idxs[i]
			}
		}
		if declared == nil {
			t.Fatalf("TableIndexes() = %+v, want idx_tpro_child_parent listed", idxs)
		}
		if len(declared.Columns) != 1 || !strings.EqualFold(declared.Columns[0], "parent_id") {
			t.Errorf("index columns = %v, want [parent_id]", declared.Columns)
		}
		if declared.Unique {
			t.Error("idx_tpro_child_parent reported UNIQUE, the fixture declares a plain index")
		}
		// The key is what makes the result grid writable and the pager stable,
		// so a catalog that hides it costs the user both.
		if primary == nil {
			t.Errorf("TableIndexes() = %+v, want the primary key index reported Primary", idxs)
		}
	})

	t.Run("quick open finds the fixture by fragment", func(t *testing.T) {
		matches, err := store.SearchInDatabase(conn, dbName, "tpro_chi", 50)
		if err != nil {
			t.Fatalf("SearchInDatabase: %v", err)
		}
		hits := map[string]string{}
		for _, m := range matches {
			hits[strings.ToLower(m.Table)] = m.Type
		}
		for _, want := range []string{"tpro_child", "tpro_child_view"} {
			if _, ok := hits[want]; !ok {
				t.Errorf("search = %+v, want %q among the hits", matches, want)
			}
		}
		for _, m := range matches {
			if m.ConnID != "contract" {
				t.Errorf("hit %+v stamped with conn %q, want contract", m, m.ConnID)
			}
			if m.Database != dbName {
				t.Errorf("hit %+v filed under database %q, want %q", m, m.Database, dbName)
			}
		}
	})

	t.Run("qualified names address the fixture", func(t *testing.T) {
		qualified, err := TableName(engine, dbName, schema, "tpro_child")
		if err != nil {
			t.Fatalf("TableName: %v", err)
		}
		if !strings.Contains(strings.ToLower(qualified), "tpro_child") {
			t.Errorf("TableName() = %q, want the child table named", qualified)
		}
		// Whatever the client shows as the table's address must be the address
		// the server answers to - and the row cap has to bind through it.
		res := store.Execute(context.Background(), conn, dbName, "SELECT id FROM "+qualified, 1)
		if res.Error != "" {
			t.Fatalf("qualified name %q does not address the fixture: %s", qualified, res.Error)
		}
		if res.RowCount != 1 {
			t.Fatalf("qualified name %q returned %d rows, want the capped read to reach the table", qualified, res.RowCount)
		}
	})

	t.Run("rebuilt DDL reports the constraint the catalog found", func(t *testing.T) {
		ddl, err := store.TableDDL("contract", dbName, schema, "tpro_child")
		if err != nil {
			t.Fatalf("TableDDL: %v", err)
		}
		lowered := strings.ToLower(ddl)
		for _, want := range []string{"tpro_child", "parent_id", "score", "primary key", "tpro_parent", "idx_tpro_child_parent"} {
			if !strings.Contains(lowered, want) {
				t.Errorf("DDL = %q, want it to carry %q", ddl, want)
			}
		}
		// TableDDL is documented as a reading aid (ddl.go), not a migration
		// tool, so its text is not re-executed here: an engine that refuses a
		// named PRIMARY KEY would fail for reasons the promise does not cover.
		// What is under test is that every catalog read reached the page.
	})
}

func TestContractMySQLPagingWindows(t *testing.T)    { testPagingContract(t, DbTypeMySQL) }
func TestContractPostgresPagingWindows(t *testing.T) { testPagingContract(t, DbTypePostgres) }

// testPagingContract is the correctness claim the whole pager rests on: with a
// stable order, the windows of one statement are disjoint and together cover the
// rows. A dialect whose OFFSET syntax the server rejects, or whose page size
// binds differently, fails here rather than in front of a user.
func testPagingContract(t *testing.T, engine string) {
	store, target := requireContractTarget(t, engine)
	conn, err := store.OpenConn("contract")
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	ctx := context.Background()
	stmt := fmt.Sprintf("SELECT id, name FROM %s ORDER BY id", contractObject(engine, target.schema, "tpro_parent"))
	if !Pageable(engine, stmt) {
		t.Fatalf("Pageable(%q) = false, the fixture statement must be pageable", stmt)
	}

	seen := map[string]bool{}
	for pageNo := 1; ; pageNo++ {
		res := store.ExecutePage(ctx, conn, target.database, stmt, pageNo, contractPageSize)
		if res.Error != "" {
			t.Fatalf("page %d: %s", pageNo, res.Error)
		}
		for _, row := range res.Rows {
			key := ValueString(row[1])
			if seen[key] {
				t.Fatalf("page %d re-served %q that an earlier page already showed", pageNo, key)
			}
			seen[key] = true
		}
		if len(res.Rows) < contractPageSize {
			break
		}
		if pageNo > 10 {
			t.Fatalf("paging never ended after %d pages (%d rows seen)", pageNo, len(seen))
		}
	}
	if len(seen) != contractRows {
		t.Errorf("windows covered %d rows, want the %d seeded", len(seen), contractRows)
	}
	// A window is the row window the UI labels, not merely a count: name order
	// is id order here, so the first page must read parent-1..parent-10.
	if !seen["parent-1"] || !seen["parent-"+strconv.Itoa(contractRows)] {
		t.Errorf("windows missed an endpoint of the key order (saw %d distinct names)", len(seen))
	}

	// The row cap is the other half of the same injection path.
	capped := store.Execute(ctx, conn, target.database,
		fmt.Sprintf("SELECT id, name FROM %s", contractObject(engine, target.schema, "tpro_parent")), 5)
	if capped.Error != "" {
		t.Fatalf("capped query: %s", capped.Error)
	}
	if capped.RowCount != 5 {
		t.Errorf("cap 5 returned %d rows, want 5", capped.RowCount)
	}
	// 25 rows are seeded, so those five are a prefix and the server round trip
	// has to say so - the local suite proves the renderer, this proves the
	// engine really sends a sixth row for the probe to find.
	if !capped.Truncated {
		t.Errorf("cap 5 over %d seeded rows reported no cut", contractRows)
	}
	if got := JudgeRowCap(engine, stmt, 5); got != RowCapApplied {
		t.Errorf("JudgeRowCap on an ORDER BY statement = %d, want it to report the cap binding", got)
	}

	// Values reach the grid as text, not as the driver's own representation -
	// MySQL answers VARCHAR as []byte, and the client has to fold that.
	typed := store.Execute(ctx, conn, target.database,
		fmt.Sprintf("SELECT id, name, score FROM %s ORDER BY id", contractObject(engine, target.schema, "tpro_child")), 0)
	if typed.Error != "" {
		t.Fatalf("typed query: %s", typed.Error)
	}
	if typed.RowCount != 5 {
		t.Fatalf("child rows = %d, want the 5 seeded", typed.RowCount)
	}
	if got := ValueString(typed.Rows[0][1]); got != "parent-1" {
		t.Errorf("name cell = %q, want the driver bytes rendered as text", got)
	}
	if got := strings.ToUpper(ValueString(typed.Rows[4][2])); got != "NULL" {
		t.Errorf("missing score rendered as %q, want NULL", got)
	}
	if len(typed.ColTypes) != 3 {
		t.Errorf("ColTypes = %v, want one entry per column", typed.ColTypes)
	}
}

func TestContractMySQLWritePath(t *testing.T)    { testWriteContract(t, DbTypeMySQL) }
func TestContractPostgresWritePath(t *testing.T) { testWriteContract(t, DbTypePostgres) }

// testWriteContract puts the builder's statements in front of the server they
// name. Two promises can only be settled there: whether an UPDATE that changes
// nothing still reports the row it matched - the affected-row rule the "matched
// no row, nothing was committed" guard rests on, and no engine answers it on
// another engine's behalf - and whether a batch that dies half way leaves
// nothing behind in that engine's transaction.
//
// No value type is asserted here on purpose. A read hands the grid whatever the
// driver decoded: lib/pq answers an integer column with int64, go-sql-driver
// with the packet bytes this client renders as text. Whichever it is has to
// round-trip back through the key predicate, and that is exactly what only a
// real server can tell us.
//
// What a text value reaches the server as is asserted, though, and for the same
// reason: whether an engine reads the client's literal as the bytes that were
// typed is the server's answer, not something the builder can prove about itself.
//
// The subtests run in order against one fixture and carry state forward; that
// is the sequence the grid produces, not four independent stories.
func testWriteContract(t *testing.T, engine string) {
	store, target := requireContractTarget(t, engine)
	conn, err := store.OpenConn("contract")
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	ctx := context.Background()
	parent := contractObject(engine, target.schema, "tpro_parent")
	selectAll := "SELECT id, name FROM " + parent + " ORDER BY id"

	readParent := func() QueryResult {
		t.Helper()
		res := store.Execute(ctx, conn, target.database, selectAll, 0)
		if res.Error != "" {
			t.Fatalf("read the fixture: %s", res.Error)
		}
		if res.RowCount != contractRows {
			t.Fatalf("fixture read %d rows, want the %d seeded", res.RowCount, contractRows)
		}
		return res
	}
	// nameAt re-reads the fixture so an assertion is about the stored row, never
	// about a count the client reported about itself.
	nameAt := func(i int) string {
		t.Helper()
		return ValueString(readParent().Rows[i][1])
	}
	parentSet := func(columns []string, changes ...RowChange) ChangeSet {
		return ChangeSet{
			ConnID: "contract", Database: target.database, Schema: target.schema,
			Table: "tpro_parent", Columns: columns, Changes: changes,
		}
	}

	t.Run("a read row is written back", func(t *testing.T) {
		rows := readParent()
		applied, err := store.ApplyChanges(parentSet(rows.Columns,
			RowChange{Original: rows.Rows[0], Edits: []CellEdit{{Column: "name", Value: "renamed"}}}))
		if err != nil || applied != 1 {
			t.Fatalf("apply: applied %d, err %v", applied, err)
		}
		if got := nameAt(0); got != "renamed" {
			t.Errorf("row 1 reads back %q, want the value the save wrote", got)
		}
	})

	t.Run("an unchanged value still counts the matched row", func(t *testing.T) {
		rows := readParent()
		// Row 1 holds "renamed" from the write above; typing the same text again
		// changes no bytes, which is the answer MySQL reports as zero rows by
		// default - and zero is what the guard calls a lost row.
		applied, err := store.ApplyChanges(parentSet(rows.Columns,
			RowChange{Original: rows.Rows[0], Edits: []CellEdit{{Column: "name", Value: ValueString(rows.Rows[0][1])}}}))
		if err != nil {
			t.Fatalf("a no-op edit of a row that exists must not read as a lost row: %v", err)
		}
		if applied != 1 {
			t.Errorf("applied = %d, want the matched row counted; without it every no-op save aborts the batch", applied)
		}
	})

	t.Run("a failing statement rolls the batch back", func(t *testing.T) {
		rows := readParent()
		dupe := ValueString(rows.Rows[0][0])
		applied, err := store.ApplyChanges(parentSet(rows.Columns,
			RowChange{Original: rows.Rows[1], Edits: []CellEdit{{Column: "name", Value: "should-not-survive"}}},
			// The second statement re-uses a key that exists, so the server - not
			// the builder - is the one that has to refuse.
			RowChange{Kind: RowInsert, Edits: []CellEdit{
				{Column: "id", Value: dupe}, {Column: "name", Value: "clash"}}}))
		if err == nil {
			t.Fatal("a duplicate primary key must fail the run, not report success")
		}
		// "statement 2" is execInTx's own wrapper: it proves the queue was built,
		// the transaction opened, and the server raised the error mid-batch.
		if !strings.Contains(err.Error(), "statement 2") {
			t.Errorf("err = %v, want it to name the statement that failed", err)
		}
		if applied != 0 {
			t.Errorf("applied = %d, want 0: a rolled back batch committed nothing", applied)
		}
		if got := nameAt(1); got != "parent-2" {
			t.Errorf("row 2 reads back %q after the rollback, want the seeded value", got)
		}
	})

	t.Run("a save after a rolled back batch still lands", func(t *testing.T) {
		// PostgreSQL is the engine this catches: a statement that fails inside a
		// transaction aborts it, and anything reusing the connection afterwards
		// answers "current transaction is aborted" until it is rolled back.
		rows := readParent()
		applied, err := store.ApplyChanges(parentSet(rows.Columns,
			RowChange{Original: rows.Rows[2], Edits: []CellEdit{{Column: "name", Value: "after-rollback"}}}))
		if err != nil || applied != 1 {
			t.Fatalf("apply after a rollback: applied %d, err %v", applied, err)
		}
		if got := nameAt(2); got != "after-rollback" {
			t.Errorf("row 3 reads back %q, want the retried save committed", got)
		}
	})

	t.Run("escaped text lands as the bytes that were typed", func(t *testing.T) {
		// Doubling the apostrophe is only half of a safe literal, and which half
		// is missing is engine business: MySQL reads a backslash as the escape
		// character inside a string literal, so an un-doubled one either swallows
		// the closing quote (the statement stops being the one that was built) or
		// quietly stores something else - a backslash and an n become a newline.
		// Only the server can say which of the two a client is doing.
		rows := readParent()
		const withQuote = `a\'b\c`
		const withNewline = `x\ny`
		applied, err := store.ApplyChanges(parentSet(rows.Columns,
			RowChange{Original: rows.Rows[3], Edits: []CellEdit{{Column: "name", Value: withQuote}}},
			RowChange{Original: rows.Rows[4], Edits: []CellEdit{{Column: "name", Value: withNewline}}}))
		if err != nil || applied != 2 {
			t.Fatalf("apply: applied %d, err %v", applied, err)
		}
		if got := nameAt(3); got != withQuote {
			t.Errorf("row 4 stores %q, want the %q that was typed", got, withQuote)
		}
		if got := nameAt(4); got != withNewline {
			t.Errorf("row 5 stores %q, want the %q that was typed", got, withNewline)
		}

		// Same layer on the reading side: the bar's contains test is a LIKE whose
		// pattern and ESCAPE character both go through the literal layer, and what
		// it has to find is the backslash the save above stored.
		where, err := store.TableWhere("contract", target.database, target.schema, "tpro_parent",
			[]TableFilter{{Column: "name", Op: FilterContains, Value: `\n`}})
		if err != nil {
			t.Fatalf("filter on the stored text: %v", err)
		}
		found := store.Execute(ctx, conn, target.database,
			"SELECT id, name FROM "+parent+where+" ORDER BY id", 0)
		if found.Error != "" {
			t.Fatalf("the filter bar's statement was refused by the server: %s", found.Error)
		}
		if found.RowCount != 1 {
			t.Fatalf("contains %q matched %d rows, want the one row that holds it", `\n`, found.RowCount)
		}
		if got := ValueString(found.Rows[0][1]); got != withNewline {
			t.Errorf("it matched row %q, want the row written above", got)
		}
	})

	t.Run("a decimal key reaches the server with every digit", func(t *testing.T) {
		// The parent fixture keys on INT, so the exact-decimal half of the key
		// predicate needs its own table: a NUMERIC(30,2) key whose 20 digits a
		// double cannot name, and a second row holding exactly what the float
		// path rounds those digits to. Rounded, the UPDATE lands on that other
		// row and the server still answers "one row matched" - the guard that
		// catches a vanished row has nothing to catch here, which is the whole
		// reason the digits have to go out as they were read.
		decimal := contractObject(engine, target.schema, "tpro_decimal")
		setup := []string{
			"DROP TABLE IF EXISTS " + decimal,
			"CREATE TABLE " + decimal + " (id NUMERIC(30,2) NOT NULL PRIMARY KEY, v VARCHAR(8) NOT NULL)",
			"INSERT INTO " + decimal + " (id, v) VALUES (123456789012345678.90, 'target')",
			"INSERT INTO " + decimal + " (id, v) VALUES (123456789012345680.00, 'decoy')",
			"INSERT INTO " + decimal + " (id, v) VALUES (1, 'first')",
		}
		t.Cleanup(func() {
			store.Execute(context.Background(), conn, target.database, "DROP TABLE IF EXISTS "+decimal, 0)
		})
		runContractStmts(t, store, conn, setup)

		readDecimal := func() QueryResult {
			t.Helper()
			res := store.Execute(ctx, conn, target.database, "SELECT id, v FROM "+decimal+" ORDER BY id", 0)
			if res.Error != "" {
				t.Fatalf("read the decimal fixture: %s", res.Error)
			}
			if res.RowCount != 3 {
				t.Fatalf("decimal fixture read %d rows, want the 3 seeded", res.RowCount)
			}
			return res
		}
		rows := readDecimal()
		// Both drivers answer an exact decimal with bytes, so the grid holds the
		// key as text - which is the shape whose digits used to be rebuilt from a
		// float64.
		if got := ValueString(rows.Rows[1][0]); got != "123456789012345678.90" {
			t.Fatalf("the seeded key reads back %q, want the digits that were inserted", got)
		}
		applied, err := store.ApplyChanges(ChangeSet{
			ConnID: "contract", Database: target.database, Schema: target.schema,
			Table: "tpro_decimal", Columns: rows.Columns,
			Changes: []RowChange{{Original: rows.Rows[1], Edits: []CellEdit{{Column: "v", Value: "hit"}}}},
		})
		if err != nil || applied != 1 {
			t.Fatalf("apply on a decimal key: applied %d, err %v", applied, err)
		}
		after := readDecimal()
		if got := ValueString(after.Rows[1][1]); got != "hit" {
			t.Errorf("the row keyed %s reads %q: the key did not reach the server as it was read",
				ValueString(after.Rows[1][0]), got)
		}
		if got := ValueString(after.Rows[2][1]); got != "decoy" {
			t.Errorf("the write landed on the row keyed %s instead - a decimal key was rounded",
				ValueString(after.Rows[2][0]))
		}
	})
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
