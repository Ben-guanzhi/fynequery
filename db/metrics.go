package db

// 可用性指标（S-01）：契约在内核仓库 `dbcore/spec/usability-metrics.json`，
// 实现共用 `dbcore/usability`。驱动层只负责把「一次任务尝试」记下来，不改变行为：
// 记录器未装上时 record 是 no-op。
//
// 记录器由 main 装上（它知道 configDir），因此这里不猜路径。

import (
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dbcore/dbcore/usability"
)

var (
	metricsMu sync.RWMutex
	metrics   *usability.Recorder
)

// UseMetrics 装上可用性记录器；nil 表示不采集。
func UseMetrics(r *usability.Recorder) {
	metricsMu.Lock()
	metrics = r
	metricsMu.Unlock()
}

func record(task, outcome string, started time.Time, fields map[string]string) {
	metricsMu.RLock()
	r := metrics
	metricsMu.RUnlock()
	if r == nil {
		return
	}
	r.Record(task, outcome, time.Since(started), fields)
}

// stmtKeyword 取语句首个关键字（SELECT/INSERT/…）作为 statement_type。
func stmtKeyword(sql string) string {
	s := strings.TrimLeftFunc(sql, func(r rune) bool {
		return unicode.IsSpace(r) || r == '(' || r == '/' || r == '*' || r == '-'
	})
	i := 0
	for i < len(s) && (s[i] == '_' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		i++
	}
	if i == 0 {
		return "unknown"
	}
	if i > 12 {
		i = 12
	}
	return strings.ToUpper(s[:i])
}
