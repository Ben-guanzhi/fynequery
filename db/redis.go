package db

// Built-in Redis driver. Redis has no SQL, so this session adapts
// dbcore.KVDriver to the table-shaped UI: keys appear as sidebar objects, a
// key's value is browsed as a grid, and the query editor sends raw commands
// (one per line, e.g. "SET greeting \"hello world\"").

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	goredis "github.com/redis/go-redis/v9"

	"github.com/dbcore/dbcore"
	dbcoreredis "github.com/dbcore/dbcore/redis"

	"fynequery/proto"
)

func init() { Register(redisDialer{}) }

const (
	redisDefaultPort = "6379"
	// redisKeyPageLimit caps one inventory page so a database with millions of
	// keys cannot stall the sidebar. The SCAN cursor is kept, so the user can
	// append further pages from the sidebar ("load more"); narrowing the
	// server-side MATCH filter is the other way to reach specific keys.
	redisKeyPageLimit = 5000
	redisScanPageSize = 500
	// redisKeyCacheTTL absorbs the repeated Tables() calls the workspace makes
	// (inventory, approxRows, quick-open) without hiding command-driven edits,
	// which invalidate the cache immediately.
	redisKeyCacheTTL = 2 * time.Second
	// redisCallTimeout bounds every KV round-trip: these calls serve the UI
	// from background goroutines, and an unbounded call against a hung server
	// parked the worker (and whatever refresh it was doing) forever.
	redisCallTimeout = 30 * time.Second
)

// redisCtx returns a bounded context for one KV call. Every kv.* call in this
// file must go through it — never context.Background() directly.
func redisCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), redisCallTimeout)
}

type redisDialer struct{}

func (redisDialer) Info() DriverInfo {
	return DriverInfo{Code: "redis", Name: "Redis", Version: "builtin", DefaultPort: redisDefaultPort}
}

func (redisDialer) Connect(p proto.ConnectParams) (Session, error) {
	idx, err := redisDBIndex(p.Database)
	if err != nil {
		return nil, err
	}
	port := p.Port
	if port == "" {
		port = redisDefaultPort
	}
	opts := &goredis.Options{
		Addr:        net.JoinHostPort(p.Host, port),
		Username:    p.User,
		Password:    p.Password,
		DB:          idx,
		DialTimeout: 10 * time.Second,
	}
	kv, err := dbcoreredis.OpenOptions(opts)
	if err != nil {
		return nil, fmt.Errorf("redis %s: %w", opts.Addr, err)
	}
	s := &redisSession{kv: kv}
	s.server = s.readServer()
	return s, nil
}

// redisDBIndex parses the connection's database field as a Redis logical DB
// number; an empty field means DB 0.
func redisDBIndex(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("redis: database must be a non-negative number, got %q", value)
	}
	return n, nil
}

type redisSession struct {
	kv     dbcore.KVDriver
	server string

	mu      sync.Mutex
	pattern string // MATCH pattern; "" means all keys
	keys    []string
	keysAt  time.Time
	cursor  uint64 // SCAN cursor for the next page; 0 = keyspace exhausted
	more    bool   // a further page exists after the cached keys
	loading bool   // a LoadMoreKeys scan is in flight
	gen     int    // bumped whenever the cached inventory is reset
}

func (s *redisSession) Server() string { return s.server }
func (s *redisSession) Close()         { _ = s.kv.Close() }

// Ping lets the workspace keepalive probe the server without a SQL statement.
func (s *redisSession) Ping() error {
	ctx, cancel := redisCtx()
	defer cancel()
	return s.kv.Ping(ctx)
}

func (s *redisSession) readServer() string {
	ctx, cancel := redisCtx()
	defer cancel()
	reply, err := s.kv.ExecCommand(ctx, []string{"INFO", "server"})
	if err != nil {
		return "Redis"
	}
	info := parseRedisInfo(reply.Raw)
	if v := info["redis_version"]; v != "" {
		return "Redis " + v
	}
	return "Redis"
}

func (s *redisSession) Tables() ([]proto.ObjectInfo, error) {
	keys, err := s.keyList()
	if err != nil {
		return nil, err
	}
	out := make([]proto.ObjectInfo, 0, len(keys))
	for _, key := range keys {
		// A key is browsable like a table; its synthetic columns come from the
		// value type (see Columns).
		out = append(out, proto.ObjectInfo{Name: key, Kind: "table", Rows: -1})
	}
	return out, nil
}

// SetKeyFilter narrows the key inventory with a Redis MATCH pattern. Plain
// text is treated as a substring (wrapped in *...*) so typing "user" finds
// user:1; queries containing glob metacharacters are used verbatim.
func (s *redisSession) SetKeyFilter(filter string) {
	pattern := redisMatchPattern(filter)
	s.mu.Lock()
	if s.pattern == pattern {
		s.mu.Unlock()
		return
	}
	s.pattern = pattern
	s.cursor, s.more = 0, false
	s.gen++
	s.keys, s.keysAt = nil, time.Time{}
	s.mu.Unlock()
}

func redisMatchPattern(filter string) string {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return "*"
	}
	if strings.ContainsAny(filter, "*?[") {
		return filter
	}
	return "*" + filter + "*"
}

func (s *redisSession) keyList() ([]string, error) {
	s.mu.Lock()
	if !s.keysAt.IsZero() && time.Since(s.keysAt) < redisKeyCacheTTL {
		keys := s.keys
		s.mu.Unlock()
		return keys, nil
	}
	if s.loading {
		// A LoadMoreKeys page is landing; hand out what is on screen and let
		// the next refresh pick the merged state up.
		keys := s.keys
		s.mu.Unlock()
		return keys, nil
	}
	pattern, cursor := s.pattern, s.cursor
	s.gen++
	// gen snapshot AFTER the bump: the landing check asks "did anyone else
	// invalidate the state since I started" — a filter change, a mutation
	// invalidation or a newer refresh all bump gen again and void this scan.
	gen := s.gen
	// Continue the previous scan instead of restarting it from cursor 0: a
	// cache refresh fires on every sidebar reload (rename, delete, manual),
	// and a restart used to wipe the pages the user had already paged in via
	// 加载更多 — cursor back to zero, keys 2..N gone. In-app mutations go
	// through invalidateKeys (full reset), so the incremental merge never
	// hides a key this app deleted; changes made outside it line up on the
	// next filter change or reconnect.
	known := make(map[string]bool, len(s.keys))
	for _, key := range s.keys {
		known[key] = true
	}
	existing := s.keys
	s.mu.Unlock()
	if pattern == "" {
		pattern = "*"
	}

	// SCAN can take a while on a large keyspace, so it runs outside the lock;
	// a concurrent SetKeyFilter simply discards this snapshot when it lands.
	scanCtx, cancel := redisCtx()
	fresh, next, more, err := scanRedisKeys(scanCtx, s.kv, cursor, pattern, known)
	cancel()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if (s.pattern == pattern || (s.pattern == "" && pattern == "*")) && s.gen == gen {
		merged := existing
		if len(fresh) > 0 {
			merged = append(append([]string(nil), existing...), fresh...)
			sort.Strings(merged)
		}
		s.keys, s.keysAt = merged, time.Now()
		s.cursor, s.more = next, more
		return merged, nil
	}
	// The filter changed or a mutation reset the state mid-scan: keep the
	// original behavior of returning what the scan found; the caller's next
	// refresh re-runs against the new state.
	return fresh, nil
}

// scanRedisKeys collects at most redisKeyPageLimit new keys from cursor,
// skipping keys already in known (SCAN may return a key more than once across
// pages). It stops after a whole SCAN page, so a page can overshoot the limit
// slightly; more reports whether the keyspace was left unfinished.
func scanRedisKeys(ctx context.Context, kv dbcore.KVDriver, cursor uint64, pattern string, known map[string]bool) ([]string, uint64, bool, error) {
	var keys []string
	for {
		page, err := kv.ScanKeys(ctx, cursor, pattern, redisScanPageSize)
		if err != nil {
			return nil, 0, false, err
		}
		for _, key := range page.Keys {
			if known != nil {
				if known[key] {
					continue
				}
				known[key] = true
			}
			keys = append(keys, key)
		}
		cursor = page.Cursor
		if cursor == 0 || len(keys) >= redisKeyPageLimit {
			break
		}
	}
	return keys, cursor, cursor != 0, nil
}

// MoreKeys reports whether another key page can be appended.
func (s *redisSession) MoreKeys() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.more
}

// LoadMoreKeys appends the next key page to the cached inventory. It is a
// no-op while a scan is already running or when the keyspace is exhausted.
func (s *redisSession) LoadMoreKeys() (int, bool, error) {
	s.mu.Lock()
	if s.loading || !s.more {
		more := s.more
		s.mu.Unlock()
		return 0, more, nil
	}
	s.loading = true
	pattern, cursor, gen := s.pattern, s.cursor, s.gen
	// The cache must be populated by keyList before paging; build the known
	// set from it so a second page cannot duplicate rows.
	known := make(map[string]bool, len(s.keys))
	for _, key := range s.keys {
		known[key] = true
	}
	existing := s.keys
	s.mu.Unlock()

	if pattern == "" {
		pattern = "*"
	}
	pageCtx, cancel := redisCtx()
	page, next, more, err := scanRedisKeys(pageCtx, s.kv, cursor, pattern, known)
	cancel()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loading = false
	if err != nil {
		return 0, s.more, err
	}
	// A filter change or an inventory reset (command, delete, rename) during
	// the scan invalidates this page.
	if s.gen != gen {
		return 0, s.more, nil
	}
	merged := append(append([]string(nil), existing...), page...)
	sort.Strings(merged)
	s.keys, s.keysAt = merged, time.Now()
	s.cursor, s.more = next, more
	return len(page), more, nil
}

// invalidateKeys drops the cached inventory after a command may have changed
// the keyspace (SET, DEL, FLUSHDB, ...).
func (s *redisSession) invalidateKeys() {
	s.mu.Lock()
	s.keys, s.keysAt = nil, time.Time{}
	s.cursor, s.more = 0, false
	s.gen++
	s.mu.Unlock()
}

func (s *redisSession) Columns(table string) ([]proto.ColumnInfo, error) {
	ctx, cancel := redisCtx()
	defer cancel()
	typ, err := s.kv.KeyType(ctx, table)
	if err != nil {
		return nil, err
	}
	cols, ok := redisColumns(typ)
	if !ok {
		return nil, fmt.Errorf("redis: key %q does not exist", table)
	}
	return cols, nil
}

// redisColumns describes how one key type flattens into grid columns. The
// identity column is marked PK: it names the element (and is never writable),
// while the remaining column — when there is one — can be overwritten in place
// via SetValue.
func redisColumns(typ dbcore.RedisKeyType) ([]proto.ColumnInfo, bool) {
	switch typ {
	case dbcore.RedisStr:
		return []proto.ColumnInfo{{Name: "key", Type: "text", Key: "PK"}, {Name: "value", Type: "text"}}, true
	case dbcore.RedisHash:
		return []proto.ColumnInfo{{Name: "field", Type: "text", Key: "PK"}, {Name: "value", Type: "text"}}, true
	case dbcore.RedisList:
		return []proto.ColumnInfo{{Name: "index", Type: "number", Key: "PK"}, {Name: "value", Type: "text"}}, true
	case dbcore.RedisSet:
		return []proto.ColumnInfo{{Name: "member", Type: "text", Key: "PK"}}, true
	case dbcore.RedisZSet:
		return []proto.ColumnInfo{{Name: "member", Type: "text", Key: "PK"}, {Name: "score", Type: "number"}}, true
	case dbcore.RedisStream:
		return []proto.ColumnInfo{{Name: "entry", Type: "text", Key: "PK"}}, true
	default:
		return nil, false
	}
}

// SetValue overwrites one element of a key in place (SET/HSET/LSET/ZADD),
// leaving the key's TTL untouched. Identity columns are rejected: a rename
// would change the grid's row key, which the SQL-style identity model cannot
// express.
func (s *redisSession) SetValue(edit ValueEdit) error {
	ctx, cancel := redisCtx()
	defer cancel()
	typ, err := s.kv.KeyType(ctx, edit.Key)
	if err != nil {
		return err
	}
	cols, ok := redisColumns(typ)
	if !ok {
		return fmt.Errorf("redis: unsupported key type %q", typ)
	}
	editable := false
	for _, c := range cols {
		if c.Name != edit.Column {
			continue
		}
		if c.Key == "PK" {
			return fmt.Errorf("redis: identity column %q cannot be edited", c.Name)
		}
		editable = true
	}
	if !editable {
		return fmt.Errorf("redis: %s keys have no editable column %q", typ, edit.Column)
	}
	ew, ok := s.kv.(dbcore.ElementWriter)
	if !ok {
		return fmt.Errorf("redis: driver does not support element writes")
	}
	return ew.SetElement(ctx, dbcore.RedisElement{
		Key: edit.Key, Identity: edit.Identity, ID: edit.ID, Column: edit.Column, Value: edit.Value,
	})
}

// KeyMeta reports a key's type and remaining lifetime.
func (s *redisSession) KeyMeta(key string) (KeyMeta, error) {
	ka, ok := s.kv.(dbcore.KeyAdmin)
	if !ok {
		return KeyMeta{}, fmt.Errorf("redis: driver does not support key administration")
	}
	metaCtx, cancel := redisCtx()
	defer cancel()
	meta, err := ka.KeyMeta(metaCtx, key)
	if err != nil {
		return KeyMeta{}, err
	}
	return KeyMeta{Kind: string(meta.Kind), TTL: meta.TTL, HasTTL: meta.HasTTL}, nil
}

// DeleteKey removes one key; missing keys are ignored, like DEL.
func (s *redisSession) DeleteKey(key string) error {
	ka, ok := s.kv.(dbcore.KeyAdmin)
	if !ok {
		return fmt.Errorf("redis: driver does not support key administration")
	}
	delCtx, cancel := redisCtx()
	defer cancel()
	if err := ka.DeleteKeys(delCtx, key); err != nil {
		return err
	}
	s.invalidateKeys()
	return nil
}

// RenameKey renames a key, refusing to overwrite an existing target.
func (s *redisSession) RenameKey(oldKey, newKey string) error {
	ka, ok := s.kv.(dbcore.KeyAdmin)
	if !ok {
		return fmt.Errorf("redis: driver does not support key administration")
	}
	renCtx, cancel := redisCtx()
	defer cancel()
	if err := ka.RenameKey(renCtx, oldKey, newKey); err != nil {
		return err
	}
	s.invalidateKeys()
	return nil
}

// SetKeyTTL sets a key's remaining lifetime; ttl <= 0 removes the expiry
// (PERSIST), so the key never expires.
func (s *redisSession) SetKeyTTL(key string, ttl time.Duration) error {
	ka, ok := s.kv.(dbcore.KeyAdmin)
	if !ok {
		return fmt.Errorf("redis: driver does not support key administration")
	}
	ttlCtx, cancel := redisCtx()
	defer cancel()
	return ka.SetTTL(ttlCtx, key, ttl)
}

// DeleteValue removes one element in place (HDEL/SREM/ZREM/XDEL), keeping the
// key's TTL. When the element was the key's last one Redis drops the key, so
// the cached inventory is invalidated to retire it from the sidebar.
func (s *redisSession) DeleteValue(edit ValueEdit) error {
	ed, ok := s.kv.(dbcore.ElementDeleter)
	if !ok {
		return fmt.Errorf("redis: driver does not support element deletes")
	}
	delCtx, cancel := redisCtx()
	defer cancel()
	if err := ed.DeleteElement(delCtx, dbcore.RedisElement{
		Key: edit.Key, Identity: edit.Identity, ID: edit.ID,
	}); err != nil {
		return err
	}
	typ, err := s.kv.KeyType(delCtx, edit.Key)
	if err == nil && typ == dbcore.RedisNone {
		s.invalidateKeys()
	}
	return nil
}

func (s *redisSession) Browse(table string, limit int) (proto.QueryResult, error) {
	start := time.Now()
	valCtx, cancel := redisCtx()
	defer cancel()
	// A bounded read when the driver has it: the value grid gets the first
	// window of the key instead of paying for a million-member collection or a
	// half-gigabyte string. limit<=0 resolves to the driver's default. The
	// truncation flag rides along on the result so the UI can say "first page"
	// instead of presenting the window as the whole key.
	var value dbcore.RedisValue
	var truncated bool
	if br, ok := dbcore.AsBoundedValueReader(s.kv); ok {
		bv, err := br.GetValueBounded(valCtx, table, dbcore.RedisValueLimit{MaxEntries: int64(limit)})
		if err != nil {
			return proto.QueryResult{}, err
		}
		value = bv.Value
		truncated = bv.Truncated
	} else {
		v, err := s.kv.GetValue(valCtx, table)
		if err != nil {
			return proto.QueryResult{}, err
		}
		value = v
	}
	cols, ok := redisColumns(value.Type)
	if !ok {
		return proto.QueryResult{}, fmt.Errorf("redis: unsupported key type %q", value.Type)
	}
	rows := redisValueRows(table, value)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		truncated = true
	}
	return proto.QueryResult{Columns: cols, Rows: taggedRowsOf(rows), DurationMS: time.Since(start).Milliseconds(), Truncated: truncated}, nil
}

func redisValueRows(key string, v dbcore.RedisValue) [][]*string {
	return redisValueRowsFrom(key, v, 0)
}

// redisValueRowsFrom renders value rows with the list's index column starting
// at listStart: a continuation window must carry each item's real rank (LSET
// edits by index — a window renumbered from zero would point an edit at the
// wrong item). Every other type ignores it.
func redisValueRowsFrom(key string, v dbcore.RedisValue, listStart int) [][]*string {
	switch v.Type {
	case dbcore.RedisStr:
		return [][]*string{{redisStr(key), redisStr(v.Str)}}
	case dbcore.RedisHash:
		fields := make([]string, 0, len(v.Hash))
		for field := range v.Hash {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		rows := make([][]*string, 0, len(fields))
		for _, field := range fields {
			rows = append(rows, []*string{redisStr(field), redisStr(v.Hash[field])})
		}
		return rows
	case dbcore.RedisList:
		rows := make([][]*string, 0, len(v.Items))
		for i, item := range v.Items {
			rows = append(rows, []*string{redisStr(strconv.Itoa(listStart + i)), redisStr(item)})
		}
		return rows
	case dbcore.RedisSet:
		rows := make([][]*string, 0, len(v.Items))
		for _, item := range v.Items {
			rows = append(rows, []*string{redisStr(item)})
		}
		return rows
	case dbcore.RedisZSet:
		rows := make([][]*string, 0, len(v.Items))
		for i, member := range v.Items {
			score := 0.0
			if i < len(v.Scores) {
				score = v.Scores[i]
			}
			rows = append(rows, []*string{redisStr(member), redisStr(strconv.FormatFloat(score, 'f', -1, 64))})
		}
		return rows
	case dbcore.RedisStream:
		rows := make([][]*string, 0, len(v.Items))
		for _, item := range v.Items {
			rows = append(rows, []*string{redisStr(item)})
		}
		return rows
	default:
		return nil
	}
}

func redisStr(s string) *string { return &s }

// Query runs newline-separated Redis commands and renders their replies as
// one text column. A trailing semicolon is tolerated so the query editor's
// SQL habits do not break commands.
func (s *redisSession) Query(text string, maxRows int) (proto.QueryResult, error) {
	start := time.Now()
	var raw strings.Builder
	ran := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSuffix(line, ";")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		args, err := splitRedisCommand(line)
		if err != nil {
			return proto.QueryResult{}, err
		}
		if len(args) == 0 {
			continue
		}
		cmdCtx, cancel := redisCtx()
		reply, err := s.kv.ExecCommand(cmdCtx, args)
		cancel()
		if err != nil {
			return proto.QueryResult{}, err
		}
		if ran > 0 {
			raw.WriteString("\n")
		}
		raw.WriteString(reply.Raw)
		ran++
	}
	if ran == 0 {
		return proto.QueryResult{}, fmt.Errorf("redis: empty command")
	}
	s.invalidateKeys()
	rows := replyRows(raw.String())
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return proto.QueryResult{
		Columns:    []proto.ColumnInfo{{Name: "reply", Type: "text"}},
		Rows:       taggedRowsOf(rows),
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// replyRows splits a rendered reply into grid rows.
func replyRows(raw string) [][]*string {
	raw = strings.TrimSuffix(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if raw == "" {
		return [][]*string{{redisStr("")}}
	}
	lines := strings.Split(raw, "\n")
	rows := make([][]*string, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, []*string{redisStr(line)})
	}
	return rows
}

// splitRedisCommand tokenizes one command, honoring single and double quotes
// and backslash escapes so values with spaces survive.
func splitRedisCommand(line string) ([]string, error) {
	var (
		out     []string
		token   strings.Builder
		quote   rune
		escaped bool
		inToken bool
	)
	flush := func() {
		if inToken {
			out = append(out, token.String())
			token.Reset()
			inToken = false
		}
	}
	for _, r := range line {
		switch {
		case escaped:
			token.WriteRune(r)
			escaped = false
			inToken = true
		case r == '\\':
			escaped = true
			inToken = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
			inToken = true
		case r == '\'' || r == '"':
			quote = r
			inToken = true
		case unicode.IsSpace(r):
			flush()
		default:
			token.WriteRune(r)
			inToken = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("redis: trailing backslash")
	}
	if quote != 0 {
		return nil, fmt.Errorf("redis: unterminated quote")
	}
	flush()
	return out, nil
}

func (s *redisSession) Metrics() ([]proto.Metric, error) {
	ctx, cancel := redisCtx()
	defer cancel()
	reply, err := s.kv.ExecCommand(ctx, []string{"INFO"})
	if err != nil {
		return nil, err
	}
	info := parseRedisInfo(reply.Raw)
	var out []proto.Metric
	add := func(group, label, key string) {
		if v := info[key]; v != "" {
			out = append(out, proto.Metric{Group: group, Label: label, Value: v})
		}
	}
	add("Server", "Version", "redis_version")
	add("Server", "Mode", "redis_mode")
	if secs, err := strconv.ParseInt(info["uptime_in_seconds"], 10, 64); err == nil {
		out = append(out, proto.Metric{Group: "Server", Label: "Uptime",
			Value: (time.Duration(secs) * time.Second).String()})
	}
	add("Clients", "Connected clients", "connected_clients")
	add("Memory", "Used memory", "used_memory_human")
	add("Stats", "Commands processed", "total_commands_processed")

	for _, line := range strings.Split(strings.ReplaceAll(reply.Raw, "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "db") || !strings.Contains(line, ":keys=") {
			continue
		}
		if name, value, ok := strings.Cut(line, ":"); ok {
			out = append(out, proto.Metric{Group: "Keyspace", Label: name, Value: value})
		}
	}
	return out, nil
}

// parseRedisInfo turns INFO's "key:value" lines into a map.
func parseRedisInfo(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			out[key] = value
		}
	}
	return out
}

// kvScanMaxRounds bounds the SCAN fill loop the same way the kernel bounds its
// own key scan: a cursor that never advances must not hang the request.
const kvScanMaxRounds = 100

// valuePageDefaultLimit is the window size when the caller passes 0 (the UI
// passes its own browseLimit).
const valuePageDefaultLimit = 300

// BrowseValuePage reads one window of a collection key through the kernel's
// KeyPageReader. cursor "" starts the walk; the returned next continues it
// ("" = exhausted). The bounded whole read had no way forward: a
// million-member key was its first 300 rows forever. Strings (one cell) and
// drivers without the capability fall back to Browse.
func (s *redisSession) BrowseValuePage(table string, cursor string, limit int) (proto.QueryResult, [][]*string, string, bool, error) {
	if limit <= 0 {
		limit = valuePageDefaultLimit
	}
	pr, ok := dbcore.AsKeyPageReader(s.kv)
	if !ok {
		res, err := s.Browse(table, limit)
		return res, nil, "", false, err
	}
	ctx, cancel := redisCtx()
	defer cancel()

	kind, err := s.kv.KeyType(ctx, table)
	if err != nil {
		return proto.QueryResult{}, nil, "", false, err
	}
	if kind == dbcore.RedisStr {
		// A string is one cell: the bounded read is its page.
		res, err := s.Browse(table, limit)
		return res, nil, "", false, err
	}
	value, next, err := redisValueWindow(ctx, pr, table, kind, cursor, limit)
	if err != nil {
		return proto.QueryResult{}, nil, "", false, err
	}
	cols, ok := redisColumns(value.Type)
	if !ok {
		return proto.QueryResult{}, nil, "", false, fmt.Errorf("redis: unsupported key type %q", value.Type)
	}
	listStart := 0
	if kind == dbcore.RedisList {
		if n, err := parseRankCursor(cursor); err == nil {
			listStart = int(n)
		}
	}
	rows := redisValueRowsFrom(table, value, listStart)
	more := next != ""
	res := proto.QueryResult{Columns: cols, Rows: taggedRowsOf(rows), Truncated: more}
	return res, rows, next, more, nil
}

// redisValueWindow mirrors the kernel pager's per-type semantics (the same
// shapes the TableProGo client renders): list/zset by rank with the length as
// the end signal, stream by last-ID with a full window as the "maybe more",
// hash/set by SCAN with a bounded fill loop, dedup and sort.
func redisValueWindow(ctx context.Context, p dbcore.KeyPageReader, key string,
	kind dbcore.RedisKeyType, cursor string, limit int) (dbcore.RedisValue, string, error) {
	switch kind {
	case dbcore.RedisList:
		start, err := parseRankCursor(cursor)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		items, length, err := p.ListRange(ctx, key, start, start+int64(limit)-1)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		next := ""
		if end := start + int64(len(items)); end < length {
			next = strconv.FormatInt(end, 10)
		}
		return dbcore.RedisValue{Type: kind, Items: items}, next, nil
	case dbcore.RedisZSet:
		start, err := parseRankCursor(cursor)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		members, scores, length, err := p.ZSetRange(ctx, key, start, start+int64(limit)-1)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		next := ""
		if end := start + int64(len(members)); end < length {
			next = strconv.FormatInt(end, 10)
		}
		return dbcore.RedisValue{Type: kind, Items: members, Scores: scores}, next, nil
	case dbcore.RedisStream:
		// StreamRange reads strictly after the ID: the cursor is the last
		// entry of the previous window ("" = from the start). A full window is
		// the only "more may follow" signal — a short one ends the walk.
		entries, _, err := p.StreamRange(ctx, key, cursor, int64(limit))
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		v := dbcore.RedisValue{Type: kind}
		for _, e := range entries {
			v.Items = append(v.Items, renderStreamEntry(e))
			v.Stream = append(v.Stream, e)
		}
		next := ""
		if len(entries) == limit {
			next = entries[len(entries)-1].ID
		}
		return v, next, nil
	case dbcore.RedisSet:
		seen := map[string]bool{}
		var members []string
		scan, err := parseScanCursor(cursor)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		for round := 0; round < kvScanMaxRounds; round++ {
			page, cont, err := p.SetPage(ctx, key, scan, int64(limit))
			if err != nil {
				return dbcore.RedisValue{}, "", err
			}
			for _, m := range page {
				if !seen[m] {
					seen[m] = true
					members = append(members, m)
				}
			}
			scan = cont
			if scan == 0 || len(members) >= limit {
				break
			}
		}
		sort.Strings(members)
		return dbcore.RedisValue{Type: kind, Items: members}, formatScanCont(scan), nil
	case dbcore.RedisHash:
		fields := map[string]string{}
		scan, err := parseScanCursor(cursor)
		if err != nil {
			return dbcore.RedisValue{}, "", err
		}
		for round := 0; round < kvScanMaxRounds; round++ {
			page, cont, err := p.HashPage(ctx, key, scan, int64(limit))
			if err != nil {
				return dbcore.RedisValue{}, "", err
			}
			for _, f := range page {
				fields[f.Name] = f.Value
			}
			scan = cont
			if scan == 0 || len(fields) >= limit {
				break
			}
		}
		return dbcore.RedisValue{Type: kind, Hash: fields}, formatScanCont(scan), nil
	default:
		return dbcore.RedisValue{}, "", fmt.Errorf("redis: cannot page %q keys", kind)
	}
}

func parseRankCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("redis: invalid window cursor %q", cursor)
	}
	return n, nil
}

func parseScanCursor(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("redis: invalid scan cursor %q", cursor)
	}
	return n, nil
}

// formatScanCont spells a SCAN continuation: cursor 0 means the walk is over.
func formatScanCont(cursor uint64) string {
	if cursor == 0 {
		return ""
	}
	return strconv.FormatUint(cursor, 10)
}

// renderStreamEntry flattens one entry into the grid's single "entry" column
// exactly as the whole-key read renders it: the ID, then the fields sorted by
// name as "k=v", space separated.
func renderStreamEntry(e dbcore.RedisStreamEntry) string {
	fields := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		fields = append(fields, f.Name+"="+f.Value)
	}
	sort.Strings(fields)
	return e.ID + " " + strings.Join(fields, " ")
}
