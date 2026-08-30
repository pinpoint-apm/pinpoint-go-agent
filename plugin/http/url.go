package pphttp

import (
	"strings"
	"unicode/utf8"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// Ant style pattern kinds. Patterns are classified once at configuration time so
// that the common shapes never pay for the general matcher on the request path.
type patternKind int

const (
	patternExact         patternKind = iota // no wildcard: plain string equality
	patternPrefix                           // "prefix**": prefix test
	patternSegmentPrefix                    // "prefix*": prefix test, no '/' beyond the prefix
	patternAnt                              // everything else: token matcher
)

type tokenKind int

const (
	tokenLiteral         tokenKind = iota
	tokenStar                      // '*'  - zero or more characters within one path segment
	tokenDoubleStar                // '**' - zero or more characters across path segments
	tokenDoubleStarSlash           // '**/' - zero or more whole path segments
	tokenQuestion                  // '?'  - exactly one character, never the path separator
)

type patternToken struct {
	kind  tokenKind
	value byte
}

type httpExcludeUrl struct {
	kind          patternKind
	pattern       string
	literalPrefix string
	minLength     int
	tokens        []patternToken
}

// matchPrefix tests the two prefix shaped kinds. Exact patterns live in
// httpUrlFilter's lookup map and Ant patterns go through antMatch, so neither
// kind reaches here.
func (h *httpExcludeUrl) matchPrefix(urlPath string) bool {
	if !strings.HasPrefix(urlPath, h.literalPrefix) {
		return false
	}
	if h.kind == patternPrefix {
		return true
	}
	// patternSegmentPrefix: the tail after the prefix stays in one segment.
	return !strings.Contains(urlPath[len(h.literalPrefix):], "/")
}

func containsWildcard(s string) bool {
	return strings.ContainsAny(s, "*?")
}

func newHttpExcludeUrl(antPath string) *httpExcludeUrl {
	h := &httpExcludeUrl{pattern: antPath}

	if !containsWildcard(antPath) {
		h.kind = patternExact
		return h
	}

	if strings.HasSuffix(antPath, "**") {
		if prefix := antPath[:len(antPath)-2]; !containsWildcard(prefix) {
			h.kind = patternPrefix
			h.literalPrefix = prefix
			return h
		}
	}

	if strings.HasSuffix(antPath, "*") && !strings.HasSuffix(antPath, "**") {
		if prefix := antPath[:len(antPath)-1]; !containsWildcard(prefix) {
			h.kind = patternSegmentPrefix
			h.literalPrefix = prefix
			return h
		}
	}

	h.kind = patternAnt
	if i := strings.IndexAny(antPath, "*?"); i > 0 {
		h.literalPrefix = antPath[:i]
	}

	for i := 0; i < len(antPath); {
		c := antPath[i]
		switch {
		case c == '*' && i+1 < len(antPath) && antPath[i+1] == '*':
			if i+2 < len(antPath) && antPath[i+2] == '/' {
				h.tokens = append(h.tokens, patternToken{kind: tokenDoubleStarSlash})
				i += 3
			} else {
				h.tokens = append(h.tokens, patternToken{kind: tokenDoubleStar})
				i += 2
			}
		case c == '*':
			h.tokens = append(h.tokens, patternToken{kind: tokenStar})
			i++
		case c == '?':
			h.tokens = append(h.tokens, patternToken{kind: tokenQuestion})
			h.minLength++
			i++
		default:
			h.tokens = append(h.tokens, patternToken{kind: tokenLiteral, value: c})
			h.minLength++
			i++
		}
	}

	return h
}

// antScratchLen sizes each stack resident DP row. Longer URLs fall back to the heap.
const antScratchLen = 256

func (h *httpExcludeUrl) antCandidate(urlPath string) bool {
	return len(urlPath) >= h.minLength && strings.HasPrefix(urlPath, h.literalPrefix)
}

// antMatch runs a two row suffix DP over the compiled tokens: next[i] holds
// "the token suffix not yet processed matches urlPath[i:]".
//
// scratch must hold at least 2*(len(urlPath)+1) entries, and it arrives dirty:
// one buffer is reused across every Ant filter of a request. Only next is
// cleared here, so each case below has to write all of cur[0:len(urlPath)+1].
// A case that leaves entries untouched would read the previous filter's
// leftovers as its own DP state.
func (h *httpExcludeUrl) antMatch(urlPath string, scratch []bool) bool {
	u := len(urlPath)
	n := u + 1
	scratch = scratch[:2*n]
	cur, next := scratch[:n], scratch[n:]
	clear(next)
	next[u] = true

	for i := len(h.tokens) - 1; i >= 0; i-- {
		t := h.tokens[i]
		switch t.kind {
		case tokenLiteral:
			cur[u] = false
			for j := u - 1; j >= 0; j-- {
				cur[j] = urlPath[j] == t.value && next[j+1]
			}
		case tokenQuestion:
			clear(cur)
			for j, r := range urlPath {
				_, size := utf8.DecodeRuneInString(urlPath[j:])
				cur[j] = r != '/' && next[j+size]
			}
		case tokenStar:
			cur[u] = next[u]
			for j := u - 1; j >= 0; j-- {
				cur[j] = next[j] || (urlPath[j] != '/' && cur[j+1])
			}
		case tokenDoubleStar:
			cur[u] = next[u]
			running := next[u]
			for j := u - 1; j >= 0; j-- {
				running = running || next[j]
				cur[j] = running
			}
		case tokenDoubleStarSlash:
			cur[u] = next[u]
			running := false
			for j := u - 1; j >= 0; j-- {
				if urlPath[j] == '/' {
					running = running || next[j+1]
				}
				cur[j] = next[j] || running
			}
		}
		cur, next = next, cur
	}

	return next[0]
}

type httpUrlFilter struct {
	exact  map[string]struct{}
	prefix []*httpExcludeUrl
	ant    []*httpExcludeUrl
}

func newHttpUrlFilter() *httpUrlFilter {
	cfgFilters := trimStringSlice(pinpoint.GetConfig().StringSlice(CfgHttpServerExcludeUrl))
	return setupHttpUrlFilter(cfgFilters)
}

func setupHttpUrlFilter(cfgFilters []string) *httpUrlFilter {
	filter := &httpUrlFilter{}
	for _, u := range cfgFilters {
		if u == "" {
			pinpoint.Log("http").Warnf("%s: empty pattern is ignored", CfgHttpServerExcludeUrl)
			continue
		}
		h := newHttpExcludeUrl(u)
		pinpoint.Log("http").Debugf("%s: %s (kind: %d)", CfgHttpServerExcludeUrl, h.pattern, h.kind)
		switch h.kind {
		case patternExact:
			if filter.exact == nil {
				filter.exact = make(map[string]struct{})
			}
			filter.exact[h.pattern] = struct{}{}
		case patternAnt:
			filter.ant = append(filter.ant, h)
		default:
			filter.prefix = append(filter.prefix, h)
		}
	}

	return filter
}

func (h *httpUrlFilter) isFiltered(url string) bool {
	if _, ok := h.exact[url]; ok {
		return true
	}
	for _, f := range h.prefix {
		if f.matchPrefix(url) {
			return true
		}
	}
	// antFiltered zeroes a scratch buffer on entry, so stay out of it entirely
	// when no Ant pattern is configured.
	if len(h.ant) == 0 {
		return false
	}
	return h.antFiltered(url)
}

func (h *httpUrlFilter) antFiltered(url string) bool {
	// The request owns this buffer; a long URL grows it once and every Ant filter reuses it.
	var stack [2 * antScratchLen]bool
	scratch := stack[:]

	for _, f := range h.ant {
		if !f.antCandidate(url) {
			continue
		}
		if n := 2 * (len(url) + 1); n > len(scratch) {
			scratch = make([]bool, n)
		}
		if f.antMatch(url, scratch) {
			return true
		}
	}
	return false
}

type httpMethodFilter struct {
	excludeMethod []string
}

func newHttpExcludeMethod() *httpMethodFilter {
	cfg := trimStringSlice(pinpoint.GetConfig().StringSlice(CfgHttpServerExcludeMethod))

	return &httpMethodFilter{
		excludeMethod: cfg,
	}
}

func (h *httpMethodFilter) isExcludedMethod(method string) bool {
	for _, em := range h.excludeMethod {
		if strings.EqualFold(em, method) {
			return true
		}
	}
	return false
}
