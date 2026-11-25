package sourcemaps

import (
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// The bundle's functions (docs/symbolication.md, phase 4): where each function of the minified
// code starts and ends, and what it was called in the source. A minified stack names its
// functions by their minified names ("n.weight", "o"), and a source map says what an identifier
// was called but not which function a position is in; reading the bundle answers that. Names
// come from the map where the minifier renamed them (`var n=class` is Board), else they're as
// written (a method keeps its name): "Board.weight", "rules.onTap", "dropTile".
//
// The bundle is read token by token (tdewolff's lexer), not parsed: minified code is valid
// JavaScript, and what's needed is where functions open and close, and the names in front of
// them. A function the reading can't place is left out (its frames fall back to other names on
// the server), never guessed into another's range.

// FunctionsVersion is the version of x_lyrelog_functions written.
const FunctionsVersion = 1

// Functions is the table put into the uploaded map as x_lyrelog_functions.
type Functions struct {
	Version int         `json:"version"`
	Names   []string    `json:"names"`
	Ranges  [][5]uint32 `json:"ranges"` // start line, column, end line, column (past it), name
}

type token struct {
	tt         js.TokenType
	text       string
	start, end pos
}

// tokenize reads the bundle's significant tokens with their positions.
func tokenize(src []byte) ([]token, error) {
	l := js.NewLexer(parse.NewInputBytes(src))
	var toks []token
	var at pos
	for {
		tt, data := l.Next()
		if tt == js.ErrorToken {
			if errors.Is(l.Err(), io.EOF) {
				return toks, nil
			}
			return nil, l.Err()
		}
		if (tt == js.DivToken || tt == js.DivEqToken) && regexAllowed(toks) {
			tt, data = l.RegExp()
			if tt == js.ErrorToken {
				return nil, l.Err()
			}
		}
		start := at
		at = advance(at, data)
		switch tt {
		case js.WhitespaceToken, js.LineTerminatorToken, js.CommentToken, js.CommentLineTerminatorToken:
			continue
		}
		toks = append(toks, token{tt: tt, text: string(data), start: start, end: at})
	}
}

// advance is the position after text: lines end at \n, \r, \r\n, U+2028 and U+2029 (as
// JavaScript's), columns count UTF-16 code units (as stacks and source maps do).
func advance(p pos, text []byte) pos {
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '\n':
			p.line, p.col = p.line+1, 0
			i++
		case c == '\r':
			p.line, p.col = p.line+1, 0
			i++
			if i < len(text) && text[i] == '\n' {
				i++
			}
		case c < utf8.RuneSelf:
			p.col++
			i++
		default:
			r, size := utf8.DecodeRune(text[i:])
			i += size
			switch {
			case r == ' ' || r == ' ':
				p.line, p.col = p.line+1, 0
			case r >= 0x10000:
				p.col += 2
			default:
				p.col++
			}
		}
	}
	return p
}

// regexAllowed: a slash after these starts a regular expression, after anything else it divides.
func regexAllowed(toks []token) bool {
	if len(toks) == 0 {
		return true
	}
	switch tt := toks[len(toks)-1].tt; tt {
	case js.CloseParenToken, js.CloseBracketToken, js.CloseBraceToken, js.StringToken, js.TemplateToken,
		js.TemplateEndToken, js.RegExpToken, js.PrivateIdentifierToken, js.IncrToken, js.DecrToken,
		js.ThisToken, js.SuperToken, js.NullToken, js.TrueToken, js.FalseToken:
		return false
	default:
		return !js.IsNumeric(tt) && !js.IsIdentifier(tt)
	}
}

type frameKind int

const (
	kParen frameKind = iota
	kBracket
	kBlock
	kObject
	kClass
	kFnBody
	kTemplate
	kArrowExpr // an arrow function's expression body: not a bracket, it ends where the expression does
)

type frame struct {
	kind      frameKind
	open      int     // the opening token
	name      string  // kClass: the class's name; kObject: what the object is bound to
	fn        *fnInfo // kFnBody, kArrowExpr: the function; kParen: the function whose parameters these are
	questions int     // kArrowExpr: ?s waiting for their :
}

type fnInfo struct {
	start, end pos
	name       string
	done       bool
}

type reader struct {
	toks    []token
	named   map[pos]string
	stack   []frame
	fns     []*fnInfo
	parenOf map[int]int // a ')' to its '('
}

func (r *reader) top() *frame {
	if len(r.stack) == 0 {
		return nil
	}
	return &r.stack[len(r.stack)-1]
}

func (r *reader) tok(i int) *token {
	if i < 0 || i >= len(r.toks) {
		return nil
	}
	return &r.toks[i]
}

func (r *reader) is(i int, tts ...js.TokenType) bool {
	t := r.tok(i)
	if t == nil {
		return false
	}
	for _, tt := range tts {
		if t.tt == tt {
			return true
		}
	}
	return false
}

// original is what an identifier of the bundle was called: the map's name for it, else itself.
func (r *reader) original(t *token) string {
	if t.tt == js.StringToken && len(t.text) >= 2 {
		if s, err := strconv.Unquote(`"` + strings.ReplaceAll(t.text[1:len(t.text)-1], `"`, `\"`) + `"`); err == nil {
			return s
		}
		return t.text[1 : len(t.text)-1]
	}
	if n, ok := r.named[t.start]; ok && n != "" {
		return n
	}
	return t.text
}

// closeArrows ends the expression bodies open at the top, before token i.
func (r *reader) closeArrows(i int) {
	for len(r.stack) > 0 && r.top().kind == kArrowExpr {
		r.end(r.top().fn, r.toks[i].start)
		r.stack = r.stack[:len(r.stack)-1]
	}
}

func (r *reader) end(fn *fnInfo, at pos) {
	if fn != nil && !fn.done {
		fn.end, fn.done = at, true
		r.fns = append(r.fns, fn)
	}
}

// binding is the name a function or an object starting at token i is given by what's before it:
// `x=`, `a.b.c=`, `key:` in an object, a class's field, `export default`.
func (r *reader) binding(i int) string {
	before := r.tok(i - 1)
	if before == nil {
		return ""
	}
	top := r.top()
	prefix := func(name string) string {
		if top != nil && (top.kind == kClass || top.kind == kObject) && top.name != "" {
			return top.name + "." + name
		}
		return name
	}
	switch before.tt {
	case js.EqToken:
		var parts []string
		k := i - 2
		for {
			t := r.tok(k)
			if t == nil || !js.IsIdentifierName(t.tt) && t.tt != js.PrivateIdentifierToken {
				break
			}
			if t.tt != js.ThisToken && t.text != "prototype" {
				parts = append(parts, r.original(t))
			}
			if !r.is(k-1, js.DotToken) {
				break
			}
			k -= 2
		}
		if len(parts) == 0 {
			return ""
		}
		for a, b := 0, len(parts)-1; a < b; a, b = a+1, b-1 {
			parts[a], parts[b] = parts[b], parts[a]
		}
		name := strings.Join(parts, ".")
		if top != nil && top.kind == kClass && (r.is(k-1, js.OpenBraceToken, js.SemicolonToken, js.CloseBraceToken, js.StaticToken)) {
			return prefix(name) // a class field
		}
		return name
	case js.ColonToken:
		key := r.tok(i - 2)
		if top != nil && top.kind == kObject && key != nil && (js.IsIdentifierName(key.tt) || key.tt == js.StringToken) && r.is(i-3, js.OpenBraceToken, js.CommaToken) {
			return prefix(r.original(key))
		}
	case js.DefaultToken:
		if r.is(i-2, js.ExportToken) {
			return "default"
		}
	}
	return ""
}

// objectAfter: a brace after this token opens an object literal, not a block.
func objectAfter(t *token) bool {
	if t == nil {
		return false
	}
	switch t.tt {
	case js.OpenParenToken, js.OpenBracketToken, js.CommaToken, js.ColonToken, js.QuestionToken,
		js.EllipsisToken, js.TemplateStartToken, js.TemplateMiddleToken, js.ReturnToken, js.TypeofToken,
		js.VoidToken, js.DeleteToken, js.ThrowToken, js.YieldToken, js.AwaitToken, js.InToken,
		js.InstanceofToken, js.CaseToken:
		return true
	case js.IncrToken, js.DecrToken:
		return false
	}
	return js.IsOperator(t.tt)
}

var notMethodNames = map[js.TokenType]bool{js.IfToken: true, js.ForToken: true, js.WhileToken: true,
	js.SwitchToken: true, js.CatchToken: true, js.WithToken: true, js.FunctionToken: true, js.ReturnToken: true}

// ReadFunctions finds the bundle's functions; named holds the map's names by generated position.
func ReadFunctions(bundle []byte, named map[pos]string) (*Functions, error) {
	toks, err := tokenize(bundle)
	if err != nil {
		return nil, err
	}
	r := &reader{toks: toks, named: named, parenOf: map[int]int{}}
	var header *fnInfo // the function whose parameters the next '(' at headerAt opens
	headerAt := -1
	var body *fnInfo // the function whose body a '{' at bodyAt opens
	bodyAt := -1
	var class *string // the class whose body the next '{' at this level opens
	classDepth := -1
	for i := range toks {
		t := &toks[i]
		if i != bodyAt {
			body = nil
		}
		switch t.tt {
		case js.OpenParenToken:
			f := frame{kind: kParen, open: i}
			if i == headerAt {
				f.fn = header
			}
			r.stack = append(r.stack, f)
		case js.OpenBracketToken:
			r.stack = append(r.stack, frame{kind: kBracket, open: i})
		case js.TemplateStartToken:
			r.stack = append(r.stack, frame{kind: kTemplate, open: i})
		case js.TemplateMiddleToken:
			r.closeArrows(i)
		case js.TemplateEndToken:
			r.closeArrows(i)
			if top := r.top(); top != nil && top.kind == kTemplate {
				r.stack = r.stack[:len(r.stack)-1]
			}
		case js.OpenBraceToken:
			switch {
			case body != nil:
				r.stack = append(r.stack, frame{kind: kFnBody, open: i, fn: body})
				body = nil
			case class != nil && classDepth == len(r.stack):
				r.stack = append(r.stack, frame{kind: kClass, open: i, name: *class})
				class = nil
			case objectAfter(r.tok(i - 1)):
				r.stack = append(r.stack, frame{kind: kObject, open: i, name: r.binding(i)})
			default:
				r.stack = append(r.stack, frame{kind: kBlock, open: i})
			}
		case js.CloseParenToken, js.CloseBracketToken, js.CloseBraceToken:
			r.closeArrows(i)
			if len(r.stack) == 0 {
				continue
			}
			f := r.stack[len(r.stack)-1]
			r.stack = r.stack[:len(r.stack)-1]
			switch {
			case f.kind == kFnBody:
				r.end(f.fn, t.end)
			case f.kind == kParen:
				r.parenOf[i] = f.open
				if f.fn != nil && r.is(i+1, js.OpenBraceToken) {
					body, bodyAt = f.fn, i+1
				}
			}
		case js.CommaToken, js.SemicolonToken:
			r.closeArrows(i)
		case js.QuestionToken:
			if top := r.top(); top != nil && top.kind == kArrowExpr {
				top.questions++
			}
		case js.ColonToken:
			for len(r.stack) > 0 && r.top().kind == kArrowExpr {
				if r.top().questions > 0 {
					r.top().questions--
					break
				}
				r.end(r.top().fn, t.start)
				r.stack = r.stack[:len(r.stack)-1]
			}
		case js.FunctionToken:
			first := i
			if r.is(i-1, js.AsyncToken) {
				first = i - 1
			}
			j := i + 1
			if r.is(j, js.MulToken) {
				j++
			}
			fn := &fnInfo{start: toks[first].start}
			if n := r.tok(j); n != nil && js.IsIdentifierName(n.tt) && r.is(j+1, js.OpenParenToken) {
				fn.name = r.original(n)
				j++
			} else {
				fn.name = r.binding(first)
			}
			header, headerAt = fn, j
		case js.ClassToken:
			// A class expression is named by what it's bound to (`var t=class r{`: esbuild names the
			// class itself _Board, for its own static members, and binds it to Board), a
			// declaration by its name.
			name := r.binding(i)
			if n := r.tok(i + 1); name == "" && n != nil && js.IsIdentifier(n.tt) {
				name = r.original(n)
			}
			class, classDepth = &name, len(r.stack)
		case js.ArrowToken:
			first := i - 1
			if r.is(first, js.CloseParenToken) {
				open, ok := r.parenOf[first]
				if !ok {
					continue
				}
				first = open
			}
			if first < 0 {
				continue
			}
			if r.is(first-1, js.AsyncToken) {
				first--
			}
			fn := &fnInfo{start: toks[first].start, name: r.binding(first)}
			if r.is(i+1, js.OpenBraceToken) {
				body, bodyAt = fn, i+1
			} else {
				r.stack = append(r.stack, frame{kind: kArrowExpr, open: i, fn: fn})
			}
		default:
			// A method: `name(` at the start of a class's or an object's member.
			top := r.top()
			if top == nil || top.kind != kClass && top.kind != kObject || !r.is(i+1, js.OpenParenToken) {
				continue
			}
			if !js.IsIdentifierName(t.tt) && t.tt != js.StringToken && t.tt != js.PrivateIdentifierToken && !js.IsNumeric(t.tt) || notMethodNames[t.tt] {
				continue
			}
			k := i - 1
			for r.is(k, js.StaticToken, js.AsyncToken, js.GetToken, js.SetToken, js.MulToken) {
				k--
			}
			if !(k == top.open || r.is(k, js.CommaToken, js.SemicolonToken, js.CloseBraceToken)) {
				continue
			}
			name := r.original(t)
			if top.name != "" {
				name = top.name + "." + name
			}
			header, headerAt = &fnInfo{start: toks[k+1].start, name: name}, i+1
		}
	}
	if len(toks) > 0 {
		last := toks[len(toks)-1].end
		for len(r.stack) > 0 {
			if f := r.top(); f.kind == kArrowExpr {
				r.end(f.fn, last)
			}
			r.stack = r.stack[:len(r.stack)-1]
		}
	}
	return table(r.fns), nil
}

func before(a, b pos) bool { return a.line < b.line || a.line == b.line && a.col < b.col }

// table sorts the functions (outer first where two start together) and keeps those that nest.
func table(fns []*fnInfo) *Functions {
	sort.SliceStable(fns, func(i, j int) bool {
		a, b := fns[i], fns[j]
		if a.start != b.start {
			return before(a.start, b.start)
		}
		return before(b.end, a.end)
	})
	t := &Functions{Version: FunctionsVersion, Names: []string{""}, Ranges: [][5]uint32{}}
	index := map[string]uint32{"": 0}
	var open []*fnInfo
	for _, f := range fns {
		if !before(f.start, f.end) {
			continue
		}
		for len(open) > 0 && !before(f.start, open[len(open)-1].end) {
			open = open[:len(open)-1]
		}
		if len(open) > 0 && before(open[len(open)-1].end, f.end) {
			continue // overlaps the one it starts in: not placed
		}
		n, ok := index[f.name]
		if !ok {
			n = uint32(len(t.Names))
			index[f.name] = n
			t.Names = append(t.Names, f.name)
		}
		t.Ranges = append(t.Ranges, [5]uint32{f.start.line, f.start.col, f.end.line, f.end.col, n})
		open = append(open, f)
	}
	return t
}
