// Package sqlddl parses the small subset of SQL that describes structure:
// CREATE SCHEMA, CREATE TABLE (columns and WITH properties), CREATE VIEW,
// INSERT ... SELECT (target, select-list names, FROM tables) and DROP. It is
// deliberately not a SQL parser: expressions are kept as text, and anything
// else is a statement of kind Other.
//
// A name may contain {placeholder} segments, which is how an inspector hands
// over a name that is only known at run time.
package sqlddl

import (
	"strings"
	"unicode"
)

// Kind is a statement kind.
type Kind string

const (
	CreateSchema Kind = "create_schema"
	CreateTable  Kind = "create_table"
	CreateView   Kind = "create_view"
	Insert       Kind = "insert"
	Drop         Kind = "drop"
	Other        Kind = "other"
)

// Name is a dotted, possibly qualified name.
type Name []string

// Table is the last part.
func (n Name) Table() string {
	if len(n) == 0 {
		return ""
	}
	return n[len(n)-1]
}

// Schema is the part before the table, "" when unqualified.
func (n Name) Schema() string {
	if len(n) < 2 {
		return ""
	}
	return n[len(n)-2]
}

// Catalog is the part before the schema, "" when absent.
func (n Name) Catalog() string {
	if len(n) < 3 {
		return ""
	}
	return n[len(n)-3]
}

func (n Name) String() string { return strings.Join(n, ".") }

// Column is a column definition.
type Column struct {
	Name    string
	Type    string
	NotNull bool
	Line    int
}

// SelectItem is one output of a select list.
type SelectItem struct {
	// Name is the output column name: the alias, or the column referenced
	// when the item is a bare column.
	Name string
	// Expr is the item's text without its alias.
	Expr string
	// Source is the single column the item reads, when it is just a column.
	Source string
	// TypeWordAlias is set when the alias is a SQL type name following a bare
	// column ("col" varchar): legal, and almost certainly meant as a cast.
	TypeWordAlias bool
	Line          int
}

// Statement is one parsed statement.
type Statement struct {
	Kind Kind
	// Object is TABLE, VIEW or SCHEMA for a DROP.
	Object      string
	Name        Name
	Line        int
	IfExists    bool
	Columns     []Column
	With        map[string]string
	Partitions  []string
	InsertCols  []string
	Select      []SelectItem
	From        []Name
	SelectStart int
}

// Parse splits text into statements and parses each.
func Parse(text string) []Statement {
	toks := lex(text)
	var out []Statement
	start := 0
	for i := 0; i <= len(toks); i++ {
		if i == len(toks) || toks[i].kind == tPunct && toks[i].text == ";" {
			if i > start {
				out = append(out, parseStatement(toks[start:i]))
			}
			start = i + 1
		}
	}
	return out
}

type tkind int

const (
	tIdent tkind = iota
	tQuoted
	tString
	tNumber
	tPunct
)

type token struct {
	kind tkind
	text string
	line int
}

func (t token) is(words ...string) bool {
	if t.kind != tIdent {
		return false
	}
	for _, w := range words {
		if strings.EqualFold(t.text, w) {
			return true
		}
	}
	return false
}

func (t token) punct(p string) bool { return t.kind == tPunct && t.text == p }

func identRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
}

func lex(s string) []token {
	var toks []token
	line := 1
	rs := []rune(s)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '\n':
			line++
			i++
		case unicode.IsSpace(r):
			i++
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case r == '/' && i+1 < len(rs) && rs[i+1] == '*':
			i += 2
			for i+1 < len(rs) && !(rs[i] == '*' && rs[i+1] == '/') {
				if rs[i] == '\n' {
					line++
				}
				i++
			}
			i += 2
		case r == '\'' || r == '"' || r == '`':
			start, l := i, line
			i++
			var b strings.Builder
			for i < len(rs) {
				if rs[i] == r {
					if i+1 < len(rs) && rs[i+1] == r {
						b.WriteRune(r)
						i += 2
						continue
					}
					break
				}
				if rs[i] == '\n' {
					line++
				}
				b.WriteRune(rs[i])
				i++
			}
			i++
			_ = start
			kind := tQuoted
			if r == '\'' {
				kind = tString
			}
			toks = append(toks, token{kind: kind, text: b.String(), line: l})
		case identRune(r) || r == '{':
			start := i
			depth := 0
			for i < len(rs) && (identRune(rs[i]) || rs[i] == '{' || rs[i] == '}' || depth > 0) {
				if rs[i] == '{' {
					depth++
				} else if rs[i] == '}' {
					if depth == 0 {
						break
					}
					depth--
				}
				i++
			}
			text := string(rs[start:i])
			kind := tIdent
			if unicode.IsDigit(rs[start]) && strings.IndexFunc(text, func(r rune) bool { return !unicode.IsDigit(r) && r != '.' }) < 0 {
				kind = tNumber
			}
			toks = append(toks, token{kind: kind, text: text, line: line})
		default:
			toks = append(toks, token{kind: tPunct, text: string(r), line: line})
			i++
		}
	}
	return toks
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek(k int) token {
	if p.pos+k < len(p.toks) {
		return p.toks[p.pos+k]
	}
	return token{kind: tPunct, text: ""}
}

func (p *parser) next() token {
	t := p.peek(0)
	p.pos++
	return t
}

func (p *parser) accept(words ...string) bool {
	for i, w := range words {
		if !p.peek(i).is(w) {
			return false
		}
	}
	p.pos += len(words)
	return true
}

func (p *parser) done() bool { return p.pos >= len(p.toks) }

// name reads a dotted name.
func (p *parser) name() Name {
	var n Name
	for {
		t := p.peek(0)
		if t.kind != tIdent && t.kind != tQuoted {
			return n
		}
		n = append(n, t.text)
		p.pos++
		if !p.peek(0).punct(".") {
			return n
		}
		p.pos++
	}
}

func parseStatement(toks []token) Statement {
	p := &parser{toks: toks}
	st := Statement{Kind: Other, Line: toks[0].line}
	switch {
	case p.accept("create"):
		p.accept("or", "replace")
		p.accept("external")
		switch {
		case p.accept("schema"), p.accept("database"):
			st.Kind = CreateSchema
			st.IfExists = p.accept("if", "not", "exists")
			st.Name = p.name()
			st.With = p.with()
		case p.accept("table"):
			st.Kind = CreateTable
			st.IfExists = p.accept("if", "not", "exists")
			st.Name = p.name()
			if p.peek(0).punct("(") {
				st.Columns = p.columns()
			}
			st.With = p.with()
			if p.accept("as") {
				p.selectStatement(&st)
			}
		case p.accept("view"), p.accept("materialized", "view"):
			st.Kind = CreateView
			st.IfExists = p.accept("if", "not", "exists")
			st.Name = p.name()
			p.with()
			if p.accept("as") {
				p.selectStatement(&st)
			}
		}
	case p.accept("insert"):
		p.accept("overwrite")
		if !p.accept("into") {
			p.accept("table")
		}
		st.Kind = Insert
		st.Name = p.name()
		if p.peek(0).punct("(") && !p.peek(1).is("select", "with") {
			p.next()
			for !p.done() && !p.peek(0).punct(")") {
				t := p.next()
				if t.kind == tIdent || t.kind == tQuoted {
					st.InsertCols = append(st.InsertCols, t.text)
				}
			}
			p.next()
		}
		p.selectStatement(&st)
	case p.accept("drop"):
		st.Kind = Drop
		st.Object = strings.ToUpper(p.next().text)
		st.IfExists = p.accept("if", "exists")
		st.Name = p.name()
	}
	if st.With != nil {
		if v, ok := st.With["partitioned_by"]; ok && v != "" {
			for _, part := range strings.Split(v, ",") {
				st.Partitions = append(st.Partitions, strings.TrimSpace(part))
			}
		}
	}
	return st
}

func (p *parser) columns() []Column {
	p.next() // (
	var cols []Column
	for !p.done() {
		t := p.peek(0)
		if t.punct(")") {
			p.next()
			break
		}
		if t.punct(",") {
			p.next()
			continue
		}
		if t.is("primary", "constraint", "unique", "foreign", "like") {
			p.skipItem()
			continue
		}
		col := Column{Name: p.next().text, Line: t.line}
		var typ []string
		depth := 0
		for !p.done() {
			u := p.peek(0)
			if depth == 0 && (u.punct(",") || u.punct(")")) {
				break
			}
			if depth == 0 && u.is("not") && p.peek(1).is("null") {
				col.NotNull = true
				p.pos += 2
				continue
			}
			if depth == 0 && u.is("null", "comment", "default", "with") {
				p.skipItem()
				break
			}
			if u.punct("(") {
				depth++
			} else if u.punct(")") {
				depth--
			}
			typ = append(typ, u.text)
			p.next()
		}
		col.Type = joinType(typ)
		cols = append(cols, col)
	}
	return cols
}

func joinType(parts []string) string {
	var b strings.Builder
	for i, s := range parts {
		if i > 0 && s != "(" && s != ")" && s != "," && parts[i-1] != "(" && parts[i-1] != "," {
			b.WriteByte(' ')
		}
		b.WriteString(s)
	}
	return b.String()
}

// skipItem skips to the next comma or closing parenthesis at this depth.
func (p *parser) skipItem() {
	depth := 0
	for !p.done() {
		u := p.peek(0)
		if depth == 0 && (u.punct(",") || u.punct(")")) {
			return
		}
		if u.punct("(") {
			depth++
		} else if u.punct(")") {
			depth--
		}
		p.next()
	}
}

// with reads WITH ( key = value, ... ). An ARRAY[...] value becomes its
// elements joined by commas.
func (p *parser) with() map[string]string {
	if !(p.peek(0).is("with") && p.peek(1).punct("(")) {
		return nil
	}
	p.pos += 2
	props := map[string]string{}
	for !p.done() {
		if p.peek(0).punct(")") {
			p.next()
			break
		}
		if p.peek(0).punct(",") {
			p.next()
			continue
		}
		key := strings.ToLower(p.next().text)
		if !p.peek(0).punct("=") {
			p.skipItem()
			continue
		}
		p.next()
		var vals []string
		if p.peek(0).is("array") {
			p.next()
			p.next() // [
			for !p.done() && !p.peek(0).punct("]") {
				t := p.next()
				if t.kind == tString || t.kind == tIdent || t.kind == tQuoted {
					vals = append(vals, t.text)
				}
			}
			p.next()
		} else {
			depth := 0
			for !p.done() {
				u := p.peek(0)
				if depth == 0 && (u.punct(",") || u.punct(")")) {
					break
				}
				if u.punct("(") {
					depth++
				} else if u.punct(")") {
					depth--
				}
				vals = append(vals, p.next().text)
			}
		}
		props[key] = strings.Join(vals, ",")
	}
	return props
}

var typeWords = map[string]bool{
	"varchar": true, "char": true, "double": true, "real": true, "bigint": true,
	"integer": true, "int": true, "smallint": true, "tinyint": true, "boolean": true,
	"date": true, "timestamp": true, "decimal": true, "varbinary": true, "json": true,
}

// selectStatement reads [(] [WITH ...] SELECT items FROM tables.
func (p *parser) selectStatement(st *Statement) {
	for !p.done() && !p.peek(0).is("select") {
		p.next()
	}
	if !p.accept("select") {
		return
	}
	p.accept("distinct")
	st.SelectStart = p.peek(0).line
	var item []token
	depth := 0
	flush := func() {
		if len(item) > 0 {
			st.Select = append(st.Select, selectItem(item))
		}
		item = nil
	}
	for !p.done() {
		t := p.peek(0)
		if depth == 0 && t.is("from") {
			break
		}
		if t.punct("(") {
			depth++
		} else if t.punct(")") {
			depth--
			if depth < 0 {
				break
			}
		}
		if depth == 0 && t.punct(",") {
			flush()
			p.next()
			continue
		}
		item = append(item, t)
		p.next()
	}
	flush()
	// FROM and JOIN targets at the outer level.
	depth = 0
	for !p.done() {
		t := p.next()
		switch {
		case t.punct("("):
			depth++
		case t.punct(")"):
			depth--
		case depth == 0 && (t.is("from") || t.is("join")):
			if n := p.name(); len(n) > 0 {
				st.From = append(st.From, n)
			}
		}
	}
}

func selectItem(toks []token) SelectItem {
	it := SelectItem{Line: toks[0].line}
	last := toks[len(toks)-1]
	body := toks
	if len(toks) >= 2 && toks[len(toks)-2].is("as") && (last.kind == tIdent || last.kind == tQuoted) {
		it.Name = last.text
		body = toks[:len(toks)-2]
	} else if len(toks) >= 2 && (last.kind == tIdent || last.kind == tQuoted) && !last.is("end") &&
		!toks[len(toks)-2].punct(".") {
		// An alias without AS: "expr name".
		it.Name = last.text
		body = toks[:len(toks)-1]
	}
	if len(body) == 1 && (body[0].kind == tIdent || body[0].kind == tQuoted) {
		it.Source = body[0].text
	} else if len(body) == 3 && body[1].punct(".") {
		it.Source = body[2].text
	}
	if it.Name == "" {
		it.Name = it.Source
		if len(body) == 1 && body[0].punct("*") {
			it.Name = "*"
		}
	}
	if it.Source != "" && it.Name != it.Source && last.kind == tIdent && typeWords[strings.ToLower(it.Name)] {
		it.TypeWordAlias = true
	}
	var parts []string
	for _, t := range body {
		switch t.kind {
		case tString:
			parts = append(parts, "'"+t.text+"'")
		case tQuoted:
			parts = append(parts, `"`+t.text+`"`)
		default:
			parts = append(parts, t.text)
		}
	}
	it.Expr = strings.Join(parts, " ")
	return it
}
