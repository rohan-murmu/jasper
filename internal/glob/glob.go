// Package glob implements the small path-matching dialect Jasper uses in
// decision files. stdlib path.Match has no ** support, and pulling a dependency
// for sixty lines of regexp translation is not worth it.
//
//	**/  matches zero or more leading path segments
//	**   matches anything, including separators
//	*    matches anything except a separator
//	?    matches one character except a separator
package glob

import (
	"regexp"
	"strings"
	"sync"
)

type Pattern struct {
	src string
	re  *regexp.Regexp
}

var cache sync.Map // string -> *Pattern

func Compile(pat string) (*Pattern, error) {
	if v, ok := cache.Load(pat); ok {
		return v.(*Pattern), nil
	}
	re, err := regexp.Compile("^" + translate(pat) + "$")
	if err != nil {
		return nil, err
	}
	p := &Pattern{src: pat, re: re}
	cache.Store(pat, p)
	return p, nil
}

func MustCompile(pat string) *Pattern {
	p, err := Compile(pat)
	if err != nil {
		panic(err)
	}
	return p
}

func (p *Pattern) Match(path string) bool { return p.re.MatchString(path) }
func (p *Pattern) String() string         { return p.src }

// Match compiles (cached) and matches in one call.
func Match(pat, path string) bool {
	p, err := Compile(pat)
	if err != nil {
		return false
	}
	return p.Match(path)
}

// MatchAny reports whether path matches at least one pattern. An empty list
// matches nothing, which is the safe default for `except` lists.
func MatchAny(pats []string, path string) bool {
	for _, p := range pats {
		if Match(p, path) {
			return true
		}
	}
	return false
}

func translate(pat string) string {
	var b strings.Builder
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '*':
			if i+1 < len(pat) && pat[i+1] == '*' {
				switch {
				// "**/" swallows the separator so that a/**/b matches a/b.
				case i+2 < len(pat) && pat[i+2] == '/':
					b.WriteString("(?:[^/]*/)*")
					i += 2
				// A trailing "/**" also matches the directory itself, so
				// "src/db/**" covers both src/db and src/db/pool.ts. Go imports
				// resolve to package directories, and a rule about a directory
				// should govern the directory.
				case i+1 == len(pat)-1 && strings.HasSuffix(b.String(), "/"):
					s := b.String()
					b.Reset()
					b.WriteString(s[:len(s)-1])
					b.WriteString("(?:/.*)?")
					i++
				default:
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '[', ']', '{', '}', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
