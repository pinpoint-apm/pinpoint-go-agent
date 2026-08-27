package pphttp

import (
	"strings"

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
	tokenDoubleStarSlash           // '**/' - like '**', but only while URL input remains
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

func (h *httpExcludeUrl) match(urlPath string) bool {
	switch h.kind {
	case patternExact:
		return urlPath == h.pattern
	case patternPrefix:
		return strings.HasPrefix(urlPath, h.literalPrefix)
	case patternSegmentPrefix:
		return strings.HasPrefix(urlPath, h.literalPrefix) &&
			!strings.Contains(urlPath[len(h.literalPrefix):], "/")
	default:
		return h.antMatch(urlPath)
	}
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

// antScratchLen sizes the stack resident DP rows. Longer URLs fall back to the heap.
const antScratchLen = 256

// antMatch runs a two row suffix DP over the compiled tokens: next[i] holds
// "the token suffix not yet processed matches urlPath[i:]".
func (h *httpExcludeUrl) antMatch(urlPath string) bool {
	u := len(urlPath)
	if u < h.minLength || !strings.HasPrefix(urlPath, h.literalPrefix) {
		return false
	}

	var scratch [2 * antScratchLen]bool
	var cur, next []bool
	if n := u + 1; n <= antScratchLen {
		cur, next = scratch[:n], scratch[antScratchLen:antScratchLen+n]
	} else {
		cur, next = make([]bool, n), make([]bool, n)
	}
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
			cur[u] = false
			for j := u - 1; j >= 0; j-- {
				cur[j] = urlPath[j] != '/' && next[j+1]
			}
		case tokenStar:
			cur[u] = next[u]
			for j := u - 1; j >= 0; j-- {
				cur[j] = next[j] || (urlPath[j] != '/' && cur[j+1])
			}
		case tokenDoubleStar, tokenDoubleStarSlash:
			// '**/' may skip the separator, but only while URL input remains.
			cur[u] = t.kind == tokenDoubleStar && next[u]
			running := next[u]
			for j := u - 1; j >= 0; j-- {
				running = running || next[j]
				cur[j] = running
			}
		}
		cur, next = next, cur
	}

	return next[0]
}

type httpUrlFilter struct {
	filters []*httpExcludeUrl
}

func newHttpUrlFilter() *httpUrlFilter {
	return &httpUrlFilter{
		filters: setupHttpUrlFilter(),
	}
}

func setupHttpUrlFilter() []*httpExcludeUrl {
	var filters []*httpExcludeUrl

	cfgFilters := trimStringSlice(pinpoint.GetConfig().StringSlice(CfgHttpServerExcludeUrl))

	for _, u := range cfgFilters {
		if u == "" {
			pinpoint.Log("http").Warnf("%s: empty pattern is ignored", CfgHttpServerExcludeUrl)
			continue
		}
		h := newHttpExcludeUrl(u)
		pinpoint.Log("http").Debugf("%s: %s (kind: %d)", CfgHttpServerExcludeUrl, h.pattern, h.kind)
		filters = append(filters, h)
	}

	return filters
}

func (h *httpUrlFilter) isFiltered(url string) bool {
	for _, h := range h.filters {
		if h.match(url) {
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
