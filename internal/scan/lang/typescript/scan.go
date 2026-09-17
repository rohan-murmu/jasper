// Package typescript implements scan.Language for TS/JS.
//
// Imports are extracted with a hand-written tokenizer rather than a full
// grammar. This is a deliberate trade: a real parser means CGO (tree-sitter),
// which complicates every cross-compile, and Jasper only needs to answer one
// question — "what does this file import?". The tokenizer skips comments,
// strings, templates and regex literals, so the usual false-positive sources
// (an import statement inside a comment or a string) are handled.
//
// The scan.Language seam exists so this can be swapped for tree-sitter later
// without touching anything else.
package typescript

import (
	"strings"

	"github.com/rohan/jasper/internal/scan"
)

func init() { scan.Register(Lang{}) }

type Lang struct{}

func (Lang) Name() string { return "typescript" }

var exts = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

func (Lang) Match(p string) bool {
	for _, e := range exts {
		if strings.HasSuffix(p, e) {
			return true
		}
	}
	return false
}

type tokKind uint8

const (
	tIdent tokKind = iota
	tString
	tPunct
)

type tok struct {
	kind tokKind
	val  string
	line int
}

func (Lang) Imports(src []byte) ([]scan.RawImport, error) {
	toks := tokenize(src)
	var out []scan.RawImport

	for i, t := range toks {
		if t.kind != tString {
			continue
		}
		prev, prev2 := at(toks, i-1), at(toks, i-2)

		isImport := false
		switch {
		case prev.kind == tIdent && prev.val == "from":
			isImport = true // import x from 'y' | export * from 'y'
		case prev.kind == tIdent && prev.val == "import":
			isImport = true // import 'y'  (side effect)
		case prev.kind == tPunct && prev.val == "(" && prev2.kind == tIdent &&
			(prev2.val == "require" || prev2.val == "import"):
			isImport = true // require('y') | await import('y')
		}
		if !isImport {
			continue
		}
		out = append(out, scan.RawImport{
			Spec: t.val,
			Line: t.line,
			Type: typeOnly(toks, i),
		})
	}
	return out, nil
}

// typeOnly walks back to the statement's `import`/`export` keyword and reports
// whether it was followed by `type`. Type-only imports do not exist at runtime,
// so boundary rules may choose to ignore them.
func typeOnly(toks []tok, i int) bool {
	for j := i - 1; j >= 0 && j > i-40; j-- {
		if toks[j].kind != tIdent {
			continue
		}
		switch toks[j].val {
		case "import", "export":
			return at(toks, j+1).kind == tIdent && at(toks, j+1).val == "type"
		}
	}
	return false
}

func at(toks []tok, i int) tok {
	if i < 0 || i >= len(toks) {
		return tok{kind: tPunct}
	}
	return toks[i]
}

// tokenize emits identifiers, string literals and the punctuation that matters,
// skipping comments and the interior of strings, templates and regex literals.
func tokenize(src []byte) []tok {
	var toks []tok
	line := 1
	n := len(src)

	// regexAllowed decides whether a '/' starts a regex or is division. A regex
	// may not follow a value, so anything that ends a value flips this off.
	regexAllowed := true

	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++

		case c == ' ' || c == '\t' || c == '\r':
			i++

		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}

		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					line++
				}
				i++
			}
			i += 2

		case c == '/' && regexAllowed:
			i++
			for i < n && src[i] != '/' && src[i] != '\n' {
				if src[i] == '\\' {
					i++
				}
				i++
			}
			i++
			regexAllowed = false

		case c == '\'' || c == '"':
			start := line
			val, next, nl := readString(src, i, c)
			line += nl
			toks = append(toks, tok{kind: tString, val: val, line: start})
			i = next
			regexAllowed = false

		case c == '`':
			// Template literal. Its value is never a static import specifier,
			// so it is skipped, not captured. ${...} may nest braces.
			i++
			depth := 0
			for i < n {
				if src[i] == '\n' {
					line++
				}
				if src[i] == '\\' {
					i += 2
					continue
				}
				if depth == 0 && src[i] == '`' {
					i++
					break
				}
				if src[i] == '$' && i+1 < n && src[i+1] == '{' {
					depth++
					i += 2
					continue
				}
				if depth > 0 && src[i] == '}' {
					depth--
				}
				i++
			}
			regexAllowed = false

		case isIdentStart(c):
			j := i
			for j < n && isIdentPart(src[j]) {
				j++
			}
			val := string(src[i:j])
			toks = append(toks, tok{kind: tIdent, val: val, line: line})
			i = j
			regexAllowed = keyword(val)

		default:
			if c == ')' || c == ']' || c == '}' {
				regexAllowed = false
			} else {
				regexAllowed = true
			}
			toks = append(toks, tok{kind: tPunct, val: string(c), line: line})
			i++
		}
	}
	return toks
}

func readString(src []byte, i int, quote byte) (val string, next int, newlines int) {
	var b strings.Builder
	i++ // opening quote
	for i < len(src) {
		switch src[i] {
		case '\\':
			if i+1 < len(src) {
				if src[i+1] == '\n' {
					newlines++
				}
				b.WriteByte(src[i+1])
				i += 2
				continue
			}
			i++
		case quote:
			return b.String(), i + 1, newlines
		case '\n':
			// Unterminated: bail rather than swallow the rest of the file.
			return b.String(), i, newlines
		default:
			b.WriteByte(src[i])
			i++
		}
	}
	return b.String(), i, newlines
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// keyword reports whether a regex literal may follow this identifier.
func keyword(s string) bool {
	switch s {
	case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void",
		"case", "do", "else", "yield", "await", "throw":
		return true
	}
	return false
}
