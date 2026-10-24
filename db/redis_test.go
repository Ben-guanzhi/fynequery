package db

import (
	"context"
	"net"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dbcore/dbcore"

	"fynequery/proto"
)

func TestRedisDBIndex(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		bad  bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"15", 15, false},
		{" 3 ", 3, false},
		{"abc", 0, true},
		{"-1", 0, true},
	} {
		got, err := redisDBIndex(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("redisDBIndex(%q) = %d, want error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("redisDBIndex(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
}

func TestSplitRedisCommand(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
		bad  bool
	}{
		{"GET key", []string{"GET", "key"}, false},
		{"  SET   k   v  ", []string{"SET", "k", "v"}, false},
		{`SET k "hello world"`, []string{"SET", "k", "hello world"}, false},
		{`SET k 'a "b" c'`, []string{"SET", "k", `a "b" c`}, false},
		{`SET k a\ b`, []string{"SET", "k", "a b"}, false},
		{`SET k ""`, []string{"SET", "k", ""}, false},
		{`GET "unterminated`, nil, true},
		{`GET k\`, nil, true},
	} {
		got, err := splitRedisCommand(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("splitRedisCommand(%q) = %#v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitRedisCommand(%q): %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitRedisCommand(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestParseRedisInfo(t *testing.T) {
	info := parseRedisInfo("# Server\r\nredis_version:7.4.1\r\nredis_mode:standalone\r\n\r\ndb0:keys=2,expires=0\r\n")
	if info["redis_version"] != "7.4.1" || info["redis_mode"] != "standalone" {
		t.Errorf("info = %#v", info)
	}
	if info["db0"] != "keys=2,expires=0" {
		t.Errorf("keyspace line = %q", info["db0"])
	}
}

func TestRedisSessionEndToEnd(t *testing.T) {
	mr := miniredis.RunT(t)
	if err := mr.Set("greeting", "hello"); err != nil {
		t.Fatal(err)
	}
	mr.HSet("user:1", "name", "Ada")
	mr.HSet("user:1", "role", "admin")
	if _, err := mr.RPush("queue", "first", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := mr.SAdd("tags", "x", "y"); err != nil {
		t.Fatal(err)
	}
	if _, err := mr.ZAdd("rank", 1.5, "alice"); err != nil {
		t.Fatal(err)
	}

	host, port, err := net.SplitHostPort(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := (redisDialer{}).Connect(proto.ConnectParams{Host: host, Port: port, Database: "0"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	keys := tableNames(t, sess)
	if want := []string{"greeting", "queue", "rank", "tags", "user:1"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("Tables() = %v, want %v", keys, want)
	}

	cols, err := sess.Columns("user:1")
	if err != nil {
		t.Fatal(err)
	}
	if got := columnNames(cols); !reflect.DeepEqual(got, []string{"field", "value"}) {
		t.Errorf("hash columns = %v", got)
	}

	res, err := sess.Browse("user:1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowStrings(res.Rows); !reflect.DeepEqual(got, [][]string{{"name", "Ada"}, {"role", "admin"}}) {
		t.Errorf("hash rows = %v", got)
	}

	res, err = sess.Browse("greeting", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowStrings(res.Rows); !reflect.DeepEqual(got, [][]string{{"greeting", "hello"}}) {
		t.Errorf("string rows = %v", got)
	}

	res, err = sess.Browse("queue", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowStrings(res.Rows); !reflect.DeepEqual(got, [][]string{{"0", "first"}}) {
		t.Errorf("list rows with limit = %v", got)
	}

	res, err = sess.Browse("rank", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowStrings(res.Rows); !reflect.DeepEqual(got, [][]string{{"alice", "1.5"}}) {
		t.Errorf("zset rows = %v", got)
	}

	if _, err := sess.Columns("missing"); err == nil {
		t.Error("Columns(missing) should report an error")
	}

	// Commands are line-based, honor quotes, and invalidate the key cache.
	res, err = sess.Query("SET greeting \"hello world\"\nGET greeting", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowStrings(res.Rows); !reflect.DeepEqual(got, [][]string{{"OK"}, {"hello world"}}) {
		t.Errorf("command reply = %v", got)
	}
	if err := mr.Set("added-outside", "1"); err != nil {
		t.Fatal(err)
	}
	if keys = tableNames(t, sess); !slices.Contains(keys, "added-outside") {
		t.Errorf("Tables() cache was not invalidated after a command (got %v)", keys)
	}

	if m, ok := sess.(Metricser); !ok {
		t.Error("redisSession must implement Metricser")
	} else if _, err := m.Metrics(); err != nil {
		t.Errorf("Metrics(): %v", err) // miniredis INFO is minimal but must not fail
	}

	if p, ok := sess.(Pinger); !ok {
		t.Error("redisSession must implement Pinger")
	} else if err := p.Ping(); err != nil {
		t.Errorf("Ping(): %v", err)
	}
}

func TestRedisMatchPattern(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "*"},
		{"   ", "*"},
		{"user", "*user*"},
		{"user:*", "user:*"},
		{"*greet*", "*greet*"},
		{"k?y", "k?y"},
		{"a[b", "a[b"},
	} {
		if got := redisMatchPattern(tc.in); got != tc.want {
			t.Errorf("redisMatchPattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// pagingKV serves a synthetic keyspace with real SCAN paging. miniredis ignores
// COUNT and returns every key at cursor 0, so it cannot exercise the inventory
// pager; this stub paginates like a real Redis server.
type pagingKV struct {
	dbcore.KVDriver // unused methods are never called
	keys            []string
}

func (p *pagingKV) ScanKeys(_ context.Context, cursor uint64, _ string, count int) (*dbcore.RedisScanPage, error) {
	if count <= 0 {
		count = 10
	}
	start := int(cursor)
	if start >= len(p.keys) {
		return &dbcore.RedisScanPage{}, nil
	}
	end := start + count
	if end > len(p.keys) {
		end = len(p.keys)
	}
	next := uint64(end)
	if end == len(p.keys) {
		next = 0
	}
	return &dbcore.RedisScanPage{Keys: p.keys[start:end], Cursor: next}, nil
}

func TestRedisKeyPager(t *testing.T) {
	const total = redisKeyPageLimit + 3*redisScanPageSize + 42
	keys := make([]string, total)
	for i := range keys {
		keys[i] = "k:" + strconv.Itoa(i)
	}
	sess := &redisSession{kv: &pagingKV{keys: keys}}
	kp := sess

	seen := make(map[string]bool)
	first := tableNames(t, sess)
	if len(first) != redisKeyPageLimit {
		t.Fatalf("first page = %d keys, want %d", len(first), redisKeyPageLimit)
	}
	for _, key := range first {
		seen[key] = true
	}
	if !kp.MoreKeys() {
		t.Fatal("MoreKeys() = false after the first page")
	}

	for round := 1; kp.MoreKeys(); round++ {
		if round > 20 {
			t.Fatal("paging did not terminate")
		}
		_, more, err := kp.LoadMoreKeys()
		if err != nil {
			t.Fatalf("LoadMoreKeys round %d: %v", round, err)
		}
		if more != kp.MoreKeys() {
			t.Errorf("round %d: LoadMoreKeys more=%v, MoreKeys()=%v", round, more, kp.MoreKeys())
		}
		for _, key := range tableNames(t, sess) { // cache hit: no rescan
			if seen[key] {
				continue // already counted in an earlier round
			}
			seen[key] = true
		}
	}
	if len(seen) != total {
		t.Fatalf("collected %d keys, want %d", len(seen), total)
	}
	// Exhausted: a further call is a no-op.
	if added, more, err := kp.LoadMoreKeys(); err != nil || added != 0 || more {
		t.Errorf("exhausted LoadMoreKeys = %d, %v, %v", added, more, err)
	}

	// A new filter restarts paging against the narrowed keyspace.
	sess.SetKeyFilter("k:1")
	if got := tableNames(t, sess); len(got) != redisKeyPageLimit || !kp.MoreKeys() {
		t.Fatalf("filtered page = %d keys, more=%v", len(got), kp.MoreKeys())
	}
}

func TestRedisKeyFilter(t *testing.T) {
	mr := miniredis.RunT(t)
	for _, k := range []string{"user:1", "user:2", "greeting", "queue"} {
		if err := mr.Set(k, "v"); err != nil {
			t.Fatal(err)
		}
	}
	host, port, err := net.SplitHostPort(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := (redisDialer{}).Connect(proto.ConnectParams{Host: host, Port: port, Database: "0"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	kf, ok := sess.(KeyFilter)
	if !ok {
		t.Fatal("redisSession must implement KeyFilter")
	}

	// Plain text narrows server-side as a substring.
	kf.SetKeyFilter("user")
	if got := tableNames(t, sess); !reflect.DeepEqual(got, []string{"user:1", "user:2"}) {
		t.Errorf("substring filter = %v", got)
	}
	// Glob metacharacters pass through to MATCH verbatim.
	kf.SetKeyFilter("user:*")
	if got := tableNames(t, sess); !reflect.DeepEqual(got, []string{"user:1", "user:2"}) {
		t.Errorf("glob filter = %v", got)
	}
	kf.SetKeyFilter("*e*")
	if got := tableNames(t, sess); !reflect.DeepEqual(got, []string{"greeting", "queue", "user:1", "user:2"}) {
		t.Errorf("middle glob filter = %v", got)
	}
	// Clearing restores the full inventory.
	kf.SetKeyFilter("")
	if got := tableNames(t, sess); !reflect.DeepEqual(got, []string{"greeting", "queue", "user:1", "user:2"}) {
		t.Errorf("cleared filter = %v", got)
	}
	// An unmatched filter yields no keys rather than an error.
	kf.SetKeyFilter("nothing-here")
	if got, err := sess.Tables(); err != nil || len(got) != 0 {
		t.Errorf("unmatched filter = %v, %v", got, err)
	}
}

func TestRedisSetValue(t *testing.T) {
	mr := miniredis.RunT(t)
	if err := mr.Set("greeting", "hello"); err != nil {
		t.Fatal(err)
	}
	mr.HSet("user:1", "name", "Ada")
	if _, err := mr.RPush("queue", "first", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := mr.ZAdd("rank", 1.5, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mr.SAdd("tags", "x"); err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := (redisDialer{}).Connect(proto.ConnectParams{Host: host, Port: port, Database: "0"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	w, ok := sess.(ValueWriter)
	if !ok {
		t.Fatal("redisSession must implement ValueWriter")
	}

	edits := []ValueEdit{
		{Key: "greeting", Identity: "key", ID: "greeting", Column: "value", Value: "hi"},
		{Key: "user:1", Identity: "field", ID: "name", Column: "value", Value: "Grace"},
		{Key: "queue", Identity: "index", ID: "1", Column: "value", Value: "SECOND"},
		{Key: "rank", Identity: "member", ID: "alice", Column: "score", Value: "2.5"},
	}
	for _, e := range edits {
		if err := w.SetValue(e); err != nil {
			t.Fatalf("SetValue(%+v): %v", e, err)
		}
	}

	for _, tc := range []struct {
		key  string
		want [][]string
	}{
		{"greeting", [][]string{{"greeting", "hi"}}},
		{"user:1", [][]string{{"name", "Grace"}}},
		{"queue", [][]string{{"0", "first"}, {"1", "SECOND"}}},
		{"rank", [][]string{{"alice", "2.5"}}},
	} {
		res, err := sess.Browse(tc.key, 10)
		if err != nil {
			t.Fatalf("Browse(%s): %v", tc.key, err)
		}
		if got := rowStrings(res.Rows); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s rows = %v, want %v", tc.key, got, tc.want)
		}
	}

	bad := []ValueEdit{
		{Key: "user:1", Identity: "field", ID: "name", Column: "field", Value: "x"},   // identity
		{Key: "tags", Identity: "member", ID: "x", Column: "member", Value: "y"},      // set
		{Key: "rank", Identity: "member", ID: "alice", Column: "score", Value: "n/a"}, // not a number
	}
	for _, e := range bad {
		if err := w.SetValue(e); err == nil {
			t.Errorf("SetValue(%+v) should fail", e)
		}
	}
}

func TestRedisKeyAdmin(t *testing.T) {
	mr := miniredis.RunT(t)
	if err := mr.Set("plain", "x"); err != nil {
		t.Fatal(err)
	}
	mr.HSet("user:1", "name", "Ada")
	mr.SetTTL("user:1", time.Hour)
	host, port, err := net.SplitHostPort(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := (redisDialer{}).Connect(proto.ConnectParams{Host: host, Port: port, Database: "0"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ka, ok := sess.(KeyAdmin)
	if !ok {
		t.Fatal("redisSession must implement KeyAdmin")
	}

	// Warm the inventory cache so the invalidations below are observable
	// without waiting out the 2s cache TTL.
	if got := tableNames(t, sess); !reflect.DeepEqual(got, []string{"plain", "user:1"}) {
		t.Fatalf("initial keys = %v", got)
	}

	meta, err := ka.KeyMeta("plain")
	if err != nil {
		t.Fatalf("KeyMeta(plain): %v", err)
	}
	if meta.Kind != "string" || meta.HasTTL {
		t.Errorf("plain meta = %+v, want string without TTL", meta)
	}
	meta, err = ka.KeyMeta("user:1")
	if err != nil {
		t.Fatalf("KeyMeta(user:1): %v", err)
	}
	if meta.Kind != "hash" || !meta.HasTTL || meta.TTL <= 0 || meta.TTL > time.Hour {
		t.Errorf("user:1 meta = %+v, want hash with TTL <= 1h", meta)
	}
	if _, err := ka.KeyMeta("missing"); err == nil {
		t.Error("KeyMeta(missing) should fail")
	}

	if err := ka.RenameKey("user:1", "user:2"); err != nil {
		t.Fatalf("RenameKey: %v", err)
	}
	if got := tableNames(t, sess); !reflect.DeepEqual(got, []string{"plain", "user:2"}) {
		t.Errorf("keys after rename = %v, want [plain user:2]", got)
	}
	if err := ka.RenameKey("plain", "user:2"); err == nil {
		t.Error("RenameKey onto an existing key should fail")
	}
	if v, err := mr.Get("plain"); err != nil || v != "x" {
		t.Errorf("plain changed by refused rename: %q, %v", v, err)
	}
	if err := ka.RenameKey("nope", "other"); err == nil {
		t.Error("RenameKey of a missing key should fail")
	}

	if err := ka.DeleteKey("plain"); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if got := tableNames(t, sess); !reflect.DeepEqual(got, []string{"user:2"}) {
		t.Errorf("keys after delete = %v, want [user:2]", got)
	}
	if err := ka.DeleteKey("plain"); err != nil {
		t.Errorf("DeleteKey on a missing key = %v, want nil", err)
	}
}

func TestRedisSetKeyTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	if err := mr.Set("plain", "x"); err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := (redisDialer{}).Connect(proto.ConnectParams{Host: host, Port: port, Database: "0"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ka, ok := sess.(KeyAdmin)
	if !ok {
		t.Fatal("redisSession must implement KeyAdmin")
	}

	if err := ka.SetKeyTTL("plain", time.Minute); err != nil {
		t.Fatalf("SetKeyTTL: %v", err)
	}
	meta, err := ka.KeyMeta("plain")
	if err != nil {
		t.Fatalf("KeyMeta(plain): %v", err)
	}
	if !meta.HasTTL || meta.TTL <= 0 || meta.TTL > time.Minute {
		t.Errorf("meta after SetKeyTTL = %+v, want TTL <= 1m", meta)
	}

	// Zero removes the expiry (PERSIST).
	if err := ka.SetKeyTTL("plain", 0); err != nil {
		t.Fatalf("SetKeyTTL(0): %v", err)
	}
	if meta, err = ka.KeyMeta("plain"); err != nil || meta.HasTTL {
		t.Errorf("meta after persist = %+v, %v; want no TTL", meta, err)
	}

	if err := ka.SetKeyTTL("missing", time.Second); err == nil {
		t.Error("SetKeyTTL on a missing key should fail")
	}
}

func TestRedisDeleteValue(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.HSet("user:1", "name", "Ada")
	mr.HSet("user:1", "role", "admin")
	mr.SetTTL("user:1", time.Hour)
	if _, err := mr.SAdd("tags", "x", "y"); err != nil {
		t.Fatal(err)
	}
	if _, err := mr.ZAdd("rank", 1, "low"); err != nil {
		t.Fatal(err)
	}
	if _, err := mr.ZAdd("rank", 9, "high"); err != nil {
		t.Fatal(err)
	}
	if _, err := mr.RPush("queue", "first", "second"); err != nil {
		t.Fatal(err)
	}
	if err := mr.Set("greeting", "hi"); err != nil {
		t.Fatal(err)
	}
	entryID, err := mr.XAdd("events", "1-1", []string{"f", "v"})
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := (redisDialer{}).Connect(proto.ConnectParams{Host: host, Port: port, Database: "0"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	d, ok := sess.(ValueDeleter)
	if !ok {
		t.Fatal("redisSession must implement ValueDeleter")
	}

	deletes := []ValueEdit{
		{Key: "user:1", Identity: "field", ID: "name"},
		{Key: "tags", Identity: "member", ID: "x"},
		{Key: "rank", Identity: "member", ID: "low"},
		{Key: "events", Identity: "entry", ID: entryID},
	}
	for _, e := range deletes {
		if err := d.DeleteValue(e); err != nil {
			t.Fatalf("DeleteValue(%+v): %v", e, err)
		}
	}

	for _, tc := range []struct {
		key  string
		want [][]string
	}{
		{"user:1", [][]string{{"role", "admin"}}},
		{"tags", [][]string{{"y"}}},
		{"rank", [][]string{{"high", "9"}}},
	} {
		res, err := sess.Browse(tc.key, 10)
		if err != nil {
			t.Fatalf("Browse(%s): %v", tc.key, err)
		}
		if got := rowStrings(res.Rows); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s rows = %v, want %v", tc.key, got, tc.want)
		}
	}
	if ttl := mr.TTL("user:1"); ttl <= 0 {
		t.Errorf("element delete cleared the key TTL (%v)", ttl)
	}
	res, err := sess.Browse("events", 10)
	if err != nil || len(res.Rows) != 0 {
		t.Errorf("stream rows after delete = %v, %v; want empty", rowStrings(res.Rows), err)
	}

	// Deleting the last field drops the key; the cached inventory must notice.
	if got := tableNames(t, sess); !slices.Contains(got, "user:1") {
		t.Fatalf("inventory before last delete = %v", got)
	}
	if err := d.DeleteValue(ValueEdit{Key: "user:1", Identity: "field", ID: "role"}); err != nil {
		t.Fatalf("DeleteValue(last field): %v", err)
	}
	if got := tableNames(t, sess); slices.Contains(got, "user:1") {
		t.Errorf("inventory still lists the vanished key: %v", got)
	}

	bad := []ValueEdit{
		{Key: "queue", Identity: "index", ID: "0"},         // lists are index-addressed
		{Key: "greeting", Identity: "key", ID: "greeting"}, // a string's element is the key
		{Key: "greeting", Identity: "member", ID: "x"},     // not a set/zset
	}
	for _, e := range bad {
		if err := d.DeleteValue(e); err == nil {
			t.Errorf("DeleteValue(%+v) should fail", e)
		}
	}
}

func tableNames(t *testing.T, sess Session) []string {
	t.Helper()
	objs, err := sess.Tables()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(objs))
	for i, o := range objs {
		if o.Kind != "table" {
			t.Errorf("key %q kind = %q, want table", o.Name, o.Kind)
		}
		out[i] = o.Name
	}
	return out
}

func columnNames(cols []proto.ColumnInfo) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

func rowStrings(rows [][]*string) [][]string {
	out := make([][]string, len(rows))
	for i, row := range rows {
		cells := make([]string, len(row))
		for j, cell := range row {
			if cell != nil {
				cells[j] = *cell
			}
		}
		out[i] = cells
	}
	return out
}
