package db

import "strings"

// FormatSQL rewrites a statement's layout without changing what it means:
// every clause keyword goes on a line of its own, keywords are upper-cased,
// nested parentheses indent by two spaces and stray whitespace collapses to
// single spaces. This is the reformatter TablePlus puts a button on, and the
// one thing a hand-typed script accumulates that nobody wants to fix by hand.
//
// What it never touches:
//
//   - string literals, quoted identifiers and comments come back byte for byte.
//     Rewriting inside them is how a formatter turns into an injection: a
//     keyword inside a string is data, and a comment is where people keep the
//     parts they have not decided about yet.
//   - words it does not recognize. Anything not in the tables below is emitted
//     exactly as typed - column names, function names, aliases, engine-specific
//     syntax. A half-known dialect is worse than a reformatter that leaves it
//     alone.
//   - statements are not added, dropped, split or joined, and semicolons are
//     not invented. Splitting stays db.SplitStatements' job.
//   - BEGIN...END blocks, CASE expressions and stored-procedure bodies are not
//     rearranged. Recognizing them means knowing every dialect's block syntax,
//     and this package deliberately does not - see the same limitation in
//     SplitStatements. Their insides survive intact, just re-indented where the
//     parentheses say so.
//
// It is idempotent: formatting its own output changes nothing, which is what
// makes it safe to bind to a keystroke you press twice by accident. Every
// decision below is taken from the tokens, never from the whitespace that
// happens to be there.
func FormatSQL(s string) string {
	return newFormatter().run(tokenizeSQL(s))
}

// clauseWords are single words that open their own line and get upper-cased.
//
// Every one of them is reserved in every engine this app speaks, which is part
// of why re-writing them is safe: an unquoted identifier cannot be spelled like
// one of these, and a quoted one is a quoted-identifier token that is never
// re-written. VALUES, SET and the DML heads are deliberately NOT here - engines
// disagree about whether those are free as identifiers, so they only count
// inside the statement they head (see dmlKeywords).
var clauseWords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "HAVING": true,
	"LIMIT": true, "OFFSET": true, "UNION": true, "JOIN": true, "ON": true,
	"AND": true, "OR": true, "WITH": true, "RETURNING": true,
}

// clausePhrases are the multi-word openers, matched longest first so LEFT JOIN
// wins over JOIN and GROUP BY is recognized as one thing rather than two words.
var clausePhrases = map[string]bool{
	"GROUP BY": true, "ORDER BY": true,
	"LEFT JOIN": true, "RIGHT JOIN": true, "INNER JOIN": true, "FULL JOIN": true,
	"CROSS JOIN": true, "LEFT OUTER JOIN": true, "RIGHT OUTER JOIN": true,
	"FULL OUTER JOIN": true, "UNION ALL": true,
}

// dmlKeywords only count inside the statement they head, keyed by that
// statement's first word: VALUES only means something after INSERT, and some
// engines leave these words free as identifiers, so upper-casing them
// everywhere would break SQL that runs today. SELECT is listed too, so an
// INSERT ... SELECT still recognizes the SELECT inside it.
var dmlKeywords = map[string]map[string]bool{
	"INSERT":  {"INSERT": true, "INSERT INTO": true, "VALUES": true, "SELECT": true, "UNION": true, "UNION ALL": true},
	"UPDATE":  {"UPDATE": true, "SET": true, "WHERE": true},
	"DELETE":  {"DELETE": true, "DELETE FROM": true, "WHERE": true},
	"REPLACE": {"REPLACE": true, "REPLACE INTO": true, "VALUES": true},
}

// plainWords get upper-cased where they stand - they never open a line, but
// leaving them mixed-case is what makes a formatted script look half-done.
var plainWords = map[string]bool{
	"AS": true, "ASC": true, "DESC": true, "DISTINCT": true,
	"NOT": true, "IN": true, "IS": true, "LIKE": true, "BETWEEN": true,
	"EXISTS": true, "NULL": true,
}

// isOperatorByte: one operator token holds every character that belongs to it,
// so "<=" stays one operator and never comes out as "< =".
func isOperatorByte(c byte) bool {
	return strings.IndexByte("=<>!+-*/%|&^~", c) >= 0
}

func isSQLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func isSQLWordPart(c byte) bool {
	return c == '_' || c >= 0x80 ||
		('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// sqlToken is one piece of SQL the formatter will never rewrite internally:
// its whole job is deciding what goes around these, never what goes inside.
type sqlToken struct {
	kind tokKind
	text string
}

type tokKind int

const (
	tokWord tokKind = iota
	tokOperator
	tokPunct
	tokString
	tokQuotedIdent
	tokLineComment
	tokBlockComment
	tokSpace
)

// tokenizeSQL splits a script into its unrewritable pieces. Literals and
// comments borrow the scanner SplitStatements uses, so a semicolon inside one
// of them is a character rather than a statement break: there is one opinion in
// this package about where a string ends.
func tokenizeSQL(s string) []sqlToken {
	var out []sqlToken
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		switch {
		case isSQLSpace(c):
			out = append(out, sqlToken{tokSpace, " "})
			for i < n && isSQLSpace(s[i]) {
				i++
			}
		case c == '-' && i+1 < n && s[i+1] == '-':
			j := i + 2
			for j < n && s[j] != '\n' {
				j++
			}
			if j < n {
				j++ // the newline ends the comment; nothing may follow it
			}
			out = append(out, sqlToken{tokLineComment, s[i:j]})
			i = j
		case c == '/' && i+1 < n && s[i+1] == '*':
			j := i + 2
			closed := false
			for j+1 < n {
				if s[j] == '*' && s[j+1] == '/' {
					j += 2
					closed = true
					break
				}
				j++
			}
			if !closed {
				j = n // a comment nobody closed runs to the end of the script
			}
			out = append(out, sqlToken{tokBlockComment, s[i:j]})
			i = j
		case c == '\'':
			j := skipQuoted(s, i, '\'')
			out = append(out, sqlToken{tokString, s[i:j]})
			i = j
		case c == '"' || c == '`':
			j := skipQuoted(s, i, c)
			out = append(out, sqlToken{tokQuotedIdent, s[i:j]})
			i = j
		case isSQLWordPart(c):
			j := i + 1
			for j < n {
				if isSQLWordPart(s[j]) {
					j++
					continue
				}
				// "t.col" and "1.5" are one word, so the dot never picks up
				// spacing of its own.
				if s[j] == '.' && j+1 < n && isSQLWordPart(s[j+1]) {
					j += 2
					continue
				}
				break
			}
			out = append(out, sqlToken{tokWord, s[i:j]})
			i = j
		case isOperatorByte(c):
			j := i + 1
			for j < n && isOperatorByte(s[j]) {
				j++
			}
			out = append(out, sqlToken{tokOperator, s[i:j]})
			i = j
		default:
			out = append(out, sqlToken{tokPunct, string(c)})
			i++
		}
	}
	return out
}

// sqlFormatter walks the tokens and decides what goes between them. The state
// is deliberately small: how deep the parentheses go, whether each open one put
// its contents on their own lines, what statement this token belongs to, and
// three one-token memories the spacing rules need. Enough for the layout, and
// nothing that could drift out of step with the text.
type sqlFormatter struct {
	lines []string
	cur   strings.Builder
	// hasContent says the current line already holds something, which is what
	// tells "start a new line" apart from "already at the start of one".
	hasContent bool
	depth      int
	// openBroken records, per open parenthesis, whether its contents were put
	// on lines of their own; the matching ")" then knows to close on one too.
	openBroken []bool
	// heads names, for every token position, the first word of the statement it
	// belongs to - what lets VALUES count after INSERT and nowhere else.
	heads []string
	// last is the previous token that went out; lastIsIdent says it went out as
	// a name or function rather than as a keyword. Together they decide whether
	// "COUNT(" keeps its hug and "FROM (" gains its space.
	last        *sqlToken
	lastIsIdent bool
	// expectName says the next word - and only the next word - stands where SQL
	// expects a name, so it is left as typed even when it is spelled like a
	// keyword ("SELECT select AS x").
	expectName bool
	// expectTable is expectName narrowed to the places that name a TABLE, and
	// the last such word is what a following "(" belongs to as a column list
	// ("INSERT INTO t (a, b)") rather than as a function call ("COUNT(*)").
	expectTable bool
	lastIsTable bool
	// noSpaceNext suppresses the one space that would otherwise follow a unary
	// sign, so "-1" stays a negative number.
	noSpaceNext bool
}

func newFormatter() *sqlFormatter { return &sqlFormatter{} }

const indentStep = "  "

func (f *sqlFormatter) run(tokens []sqlToken) string {
	f.heads = statementHeads(tokens)
	for i := 0; i < len(tokens); i++ {
		t := &tokens[i]
		switch t.kind {
		case tokSpace:
			continue // spacing belongs to what surrounds a token, not to it
		case tokLineComment:
			// The line break belongs to the layout now: the comment goes out
			// as typed and the join puts the newline back. Line endings come
			// out LF, which is what saving a script already does.
			f.writeVerbatim(strings.TrimRight(t.text, "\r\n"))
			f.endLine()
			f.last, f.lastIsIdent = t, false
		case tokBlockComment:
			f.writeVerbatim(t.text)
			if strings.ContainsAny(t.text, "\r\n") {
				f.endLine()
			}
			f.last, f.lastIsIdent = t, false
		default:
			if key, end, clause := clauseAt(tokens, i, f.heads[i]); clause {
				if f.expectName {
					// A name is expected here, so even a word spelled SELECT
					// is the user's column name.
					f.emit(i, tokens, t)
					break
				}
				if f.hasContent {
					f.endLine() // nothing to break away from at a fresh line
				}
				f.writeVerbatim(key)
				f.last, f.lastIsIdent = &tokens[end], false
				f.expectName, f.expectTable = nameFollows(key), tableFollows(key)
				i = end
				continue
			}
			f.emit(i, tokens, t)
		}
	}
	f.endLine()
	// endLine only keeps lines that hold something, so a script ending in a
	// comment gains no blank last line.
	return strings.Join(f.lines, "\n")
}

// emit lays out one ordinary token.
func (f *sqlFormatter) emit(i int, tokens []sqlToken, t *sqlToken) {
	// Upper-case where it stands: no line of its own, but leaving these
	// mixed-case is what makes a formatted script look half-finished. This
	// applies even where a name is expected, because these words are reserved
	// everywhere and so cannot be the name of anything unquoted.
	if upper := strings.ToUpper(t.text); t.kind == tokWord && plainWords[upper] {
		f.writeToken(&sqlToken{kind: tokWord, text: upper})
		f.lastIsIdent = false
		return
	}
	switch t.text {
	case "(":
		f.parenOpen(tokens, i, t)
	case ")":
		f.parenClose(t)
	case ";":
		f.writeToken(t)
		f.depth = 0
		f.openBroken = nil
		f.endLine()
	case "-", "+":
		f.writeToken(t)
		if f.startsNumber(tokens, i, t) {
			f.noSpaceNext = true // "-1" is one number, not a subtraction
		}
	default:
		f.writeToken(t)
	}
}

// statementHeads labels every token with the first word of its statement, so a
// decision at that token can ask "what kind of statement is this?" without
// looking around. Comments above a statement belong to what follows them.
func statementHeads(tokens []sqlToken) []string {
	heads := make([]string, len(tokens))
	head := ""
	for i, t := range tokens {
		switch {
		case t.kind == tokPunct && t.text == ";":
			head = ""
		case t.kind == tokWord && head == "":
			head = strings.ToUpper(t.text)
		}
		heads[i] = head
	}
	return heads
}

// parenOpen opens a bracket. Only a subquery turns it into a block with lines
// of its own; COUNT( and a list of literals stay where they were typed.
func (f *sqlFormatter) parenOpen(tokens []sqlToken, i int, t *sqlToken) {
	f.writeToken(t)
	f.depth++
	broken := false
	if n := nextMeaningful(tokens, i+1); n >= 0 && isSubqueryWord(tokens[n].text) {
		broken = true
	}
	f.openBroken = append(f.openBroken, broken)
	if broken {
		f.endLine()
	}
}

// parenClose puts the matching bracket back, on a line of its own when the
// bracket it closes owns one.
func (f *sqlFormatter) parenClose(t *sqlToken) {
	if len(f.openBroken) == 0 {
		f.writeToken(t) // unbalanced bracket: leave it where it is
		return
	}
	f.depth--
	broken := f.openBroken[len(f.openBroken)-1]
	f.openBroken = f.openBroken[:len(f.openBroken)-1]
	if broken {
		f.endLine()
	}
	f.writeToken(t)
}

// startsNumber reports whether this sign is the sign of a literal rather than
// an operator between two operands: nothing, a bracket, a comma or another
// operator in front of it, and digits behind it.
func (f *sqlFormatter) startsNumber(tokens []sqlToken, i int, t *sqlToken) bool {
	if len(t.text) != 1 {
		return false
	}
	n := nextMeaningful(tokens, i+1)
	if n < 0 || tokens[n].kind != tokWord {
		return false
	}
	if c := tokens[n].text[0]; c < '0' || c > '9' {
		return false
	}
	if f.last == nil {
		return true
	}
	if f.last.kind == tokOperator {
		return true
	}
	if f.last.kind == tokPunct && (f.last.text == "(" || f.last.text == ",") {
		return true
	}
	// What went before was a keyword (SELECT, WHERE, VALUES ...): "-1" here is
	// one operand, not "keyword minus one".
	return f.last.kind == tokWord && !f.lastIsIdent
}

// writeToken emits one token with the spacing that belongs in front of it.
func (f *sqlFormatter) writeToken(t *sqlToken) {
	if !f.hasContent {
		f.startLine()
	} else if !f.noSpaceNext && f.needsSpace(t) {
		f.cur.WriteString(" ")
	}
	f.cur.WriteString(t.text)
	f.hasContent = true
	f.last, f.lastIsIdent = t, t.kind == tokWord
	f.lastIsTable = t.kind == tokWord && f.expectTable
	f.expectName, f.expectTable, f.noSpaceNext = false, false, false
}

// writeVerbatim emits text that must not be altered - a comment, or the
// upper-cased spelling of a clause keyword.
func (f *sqlFormatter) writeVerbatim(text string) {
	if !f.hasContent {
		f.startLine()
	} else if f.last != nil && f.last.kind != tokSpace {
		f.cur.WriteString(" ")
	}
	f.cur.WriteString(text)
	f.hasContent = true
}

// needsSpace is the whole spacing policy: no space inside brackets, none before
// a comma, semicolon, dot or closing parenthesis, none right after an opening
// one, and none before "(" when what precedes it is a function name rather than
// a keyword.
func (f *sqlFormatter) needsSpace(t *sqlToken) bool {
	switch {
	case f.last == nil:
		return false
	case f.last.kind == tokPunct && (f.last.text == "(" || f.last.text == "."):
		return false
	case t.kind == tokPunct && (t.text == "," || t.text == ";" || t.text == ")" || t.text == "."):
		return false
	case t.text == "(":
		// COUNT( hugs; "INSERT INTO t (" does not - see expectTable.
		return !f.lastIsIdent || f.lastIsTable
	}
	return true
}

func (f *sqlFormatter) startLine() {
	f.cur.WriteString(strings.Repeat(indentStep, f.depth))
	f.hasContent = true
}

func (f *sqlFormatter) endLine() {
	if !f.hasContent {
		return
	}
	f.lines = append(f.lines, strings.TrimRight(f.cur.String(), " \t"))
	f.cur.Reset()
	f.hasContent = false
}

// nextMeaningful is the index of the next token that carries SQL.
func nextMeaningful(tokens []sqlToken, i int) int {
	for ; i < len(tokens); i++ {
		if tokens[i].kind != tokSpace {
			return i
		}
	}
	return -1
}

// isSubqueryWord: which words turn "(" into a block that owns its own lines.
// Only subqueries do; COUNT( and a list of literals stay put.
func isSubqueryWord(word string) bool {
	switch strings.ToUpper(word) {
	case "SELECT", "WITH":
		return true
	}
	return false
}

// nameFollows reports whether SQL expects a NAME directly after this clause
// keyword - the one position where a word spelled like a keyword is still the
// user's identifier ("SELECT select AS x", "FROM `from`").
// tableFollows reports whether this clause is followed by a table name, which
// is the one place a following "(" opens a column list rather than an argument
// list - hence "INSERT INTO t (a, b)" keeping its space.
func tableFollows(key string) bool {
	switch key {
	case "FROM", "UPDATE", "TABLE", "INTO", "INSERT INTO", "DELETE FROM":
		return true
	}
	return strings.HasSuffix(key, "JOIN")
}

func nameFollows(key string) bool {
	switch key {
	case "SELECT", "FROM", "UPDATE", "TABLE", "AS":
		return true
	case "INSERT INTO", "DELETE FROM", "GROUP BY", "ORDER BY":
		return true
	}
	if strings.HasSuffix(key, "JOIN") {
		return true
	}
	return false
}

// clauseAt reports the clause keyword beginning at token i and the index of its
// last token, longest match first so LEFT JOIN is one keyword rather than a
// LEFT nobody has heard of. It reports end <= i when there is no clause here.
func clauseAt(tokens []sqlToken, i int, headStmt string) (string, int, bool) {
	if tokens[i].kind != tokWord {
		return "", i, false
	}
	const maxWords = 3
	words := make([]string, 0, maxWords)
	at := make([]int, 0, maxWords)
	j := i
	for len(words) < maxWords {
		j = nextMeaningful(tokens, j)
		if j < 0 || tokens[j].kind != tokWord {
			break
		}
		words = append(words, tokens[j].text)
		at = append(at, j)
		j++
	}
	for take := len(words); take >= 1; take-- {
		phrase := strings.ToUpper(strings.Join(words[:take], " "))
		if !isClause(phrase, headStmt, take) {
			continue
		}
		// Every word of the phrase takes the phrase's place: they move to the
		// upper-cased keyword together or not at all.
		return phrase, at[take-1], true
	}
	return "", i, false
}

// isClause decides whether a phrase opens a clause here. A two-word phrase only
// counts when its words stand next to each other, which clauseAt guarantees by
// construction - "GROUP BY y" is a clause, "GROUP something BY" is not either.
func isClause(phrase, headStmt string, words int) bool {
	if words == 1 {
		return clauseWords[phrase] || dmlKeywords[headStmt][phrase]
	}
	return clausePhrases[phrase] || dmlKeywords[headStmt][phrase]
}
