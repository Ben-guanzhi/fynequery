package main

// 数据库错误的人类可读翻译：内核 dberror 分类出稳定错误码，这里在原文前补
// 一句用户能行动的中文提示（fynequery 的 UI 以中文为基调）。原始错误串永远
// 保留在后面；未分类的错误原样透出，不假装翻译。

import (
	"fmt"

	"github.com/dbcore/dbcore/dberror"
)

func friendlyErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch dberror.Classify(err) {
	case dberror.UniqueViolation:
		return fmt.Errorf("唯一键冲突：这个值已经存在，换个值或改用已存在的行。\n%s", msg)
	case dberror.ForeignKeyViolation:
		return fmt.Errorf("外键冲突：引用的行不存在，或还有行引用它（先处理引用再删改）。\n%s", msg)
	case dberror.NotNullViolation:
		return fmt.Errorf("非空约束：这一列必须有值。\n%s", msg)
	case dberror.CheckViolation:
		return fmt.Errorf("检查约束不满足。\n%s", msg)
	case dberror.Deadlock, dberror.SerializationFailure:
		return fmt.Errorf("锁冲突：重试本条操作通常可以解决。\n%s", msg)
	case dberror.ConnectionBroken:
		return fmt.Errorf("连接已断开：请重连后重试。\n%s", msg)
	case dberror.Timeout:
		return fmt.Errorf("等待超时：服务端没有在时限内响应。\n%s", msg)
	}
	return err
}
