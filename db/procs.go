package db

// 会话管理能力桥（dbcore v0.5.0 的 ProcessLister 可选能力）：fynequery 的
// session 持有 *sql.DB，这里以 dbcore 驱动现场断言内核能力——MySQL 支持
// （information_schema.PROCESSLIST + KILL）；其它引擎实现 ProcLister() 后
// 自然可用，未实现返回 nil（UI 显示"不支持"说明）。

import (
	dbcore "github.com/Ben-guanzhi/dbcore"
	dbcoremysql "github.com/Ben-guanzhi/dbcore/mysql"
)

// ProcLister 是会话管理的可选能力接口：session 实现它，UI 断言后使用。
type ProcLister interface {
	// ProcLister 返回内核能力（nil = 当前引擎不支持会话管理）。
	ProcLister() dbcore.ProcessLister
}

// ProcLister 实现（mysqlSession）：从持有的 *sql.DB 现场构造内核驱动。
func (s *mysqlSession) ProcLister() dbcore.ProcessLister {
	return dbcoremysql.NewFromDB(s.db)
}

// ProcListerFor 断言 helper：session 支持则返回能力，否则 nil。
func ProcListerFor(s Session) dbcore.ProcessLister {
	if pl, ok := s.(ProcLister); ok {
		return pl.ProcLister()
	}
	return nil
}
