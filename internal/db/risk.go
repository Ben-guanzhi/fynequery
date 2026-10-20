package db

import "strings"

// A destructive statement is one that can take data away without saying which
// data. TablePlus sells this as Safe mode; here it is the one confirmation the
// editor asks for before it sends, because everything it already guards - grid
// edits, DROP from the navigator - is guarded, and a hand-typed statement in
// the editor is the one path that is not.
//
// Three shapes it recognizes:
//
//   - DROP / TRUNCATE / ALTER ... DROP: the object is named but the rows are
//     the point, and the rows are gone either way.
//   - UPDATE / DELETE with no WHERE: the classic. The user meant to narrow it,
//     pressed Ctrl+Enter, and the server happily obliged.
//
// The deliberate limit is the WHERE test: it is a word match on the statement
// with comments and string literals stripped, so `WHERE 1 = 1` passes and a
// `WHERE` that only appears inside a quoted value does not. It is not a
// parser, and it does not try to be - a statement it cannot judge simply is
// not judged.
type DestructiveKind string

const (
	RiskNone     DestructiveKind = ""
	RiskDrop     DestructiveKind = "DROP"
	RiskTruncate DestructiveKind = "TRUNCATE"
	RiskAlter    DestructiveKind = "ALTER"
	RiskUpdate   DestructiveKind = "UPDATE"
	RiskDelete   DestructiveKind = "DELETE"
)

// DestructiveRisk classifies one statement. kind is the verb that makes it
// destructive ("" when it is not), reason is the sentence the confirmation
// dialog shows.
func DestructiveRisk(stmt string) (kind DestructiveKind, reason string) {
	// Comments and literals are invisible to this decision: a WHERE that only
	// exists inside a comment or a quoted value narrows nothing.
	code := strings.TrimSpace(sqlCommentRe.ReplaceAllString(stmt, " "))
	if code == "" {
		return RiskNone, ""
	}
	switch strings.ToUpper(firstSQLWord(code)) {
	case "DROP":
		return RiskDrop, "这条语句会连结构与数据一起删掉，且无法撤销。"
	case "TRUNCATE":
		return RiskTruncate, "TRUNCATE 会清空整张表，删掉的行无法回滚。"
	case "ALTER":
		if hasWordAtDepth(code, "DROP", 0) {
			return RiskAlter, "这条 ALTER 会从表上删掉东西（列或约束）。"
		}
	case "UPDATE":
		if !hasWordAtDepth(code, "WHERE", 0) {
			return RiskUpdate, "UPDATE 没有 WHERE，整张表的这一列都会被改掉。"
		}
	case "DELETE":
		if !hasWordAtDepth(code, "WHERE", 0) {
			return RiskDelete, "DELETE 没有 WHERE，整张表的行都会被删掉。"
		}
	}
	return RiskNone, ""
}

// firstSQLWord returns the statement's leading word, or "" when the text does
// not start with one (a script that opens with a comment has its leading word
// found after the comment, which is what a verb search wants).
func firstSQLWord(code string) string {
	i := 0
	for i < len(code) && !isSQLWordStart(code[i]) {
		i++
	}
	start := i
	for i < len(code) && isSQLWordPart(code[i]) {
		i++
	}
	if start == i {
		return ""
	}
	return code[start:i]
}

func isSQLWordStart(c byte) bool {
	return c == '_' || c >= 0x80 ||
		('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// hasWordAtDepth reports whether word appears in code at the given parenthesis
// depth. Depth matters: a WHERE buried in a subquery does not narrow the rows the
// outer statement touches.
func hasWordAtDepth(code string, word string, wantDepth int) bool {
	depth := 0
	for _, t := range tokenizeSQL(code) {
		if t.kind == tokPunct {
			switch t.text {
			case "(":
				depth++
			case ")":
				if depth > 0 {
					depth--
				}
			}
			continue
		}
		if t.kind == tokWord && depth == wantDepth && strings.EqualFold(t.text, word) {
			return true
		}
	}
	return false
}
