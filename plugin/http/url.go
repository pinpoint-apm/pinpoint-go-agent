package pphttp

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type httpExcludeUrl struct {
	pattern *regexp.Regexp
}

func (h *httpExcludeUrl) match(urlPath string) bool {
	if h.pattern != nil {
		return h.pattern.MatchString(urlPath)
	}

	return false
}

func newHttpExcludeUrl(urlPath string) *httpExcludeUrl {
	h := httpExcludeUrl{pattern: nil}

	pinpoint.Log("http").Debug("newHttpExcludeUrl: ", urlPath, convertToRegexp(urlPath))

	r, err := regexp.Compile(convertToRegexp(urlPath))
	if err == nil {
		h.pattern = r
	}
	return &h
}

func convertToRegexp(antPath string) string {
	var buf bytes.Buffer
	buf.WriteRune('^')

	afterStar := false
	for _, c := range antPath {
		if afterStar {
			if c == '*' {
				buf.WriteString(".*")
			} else {
				buf.WriteString("[^/]*")
				writeRune(&buf, c)
			}
			afterStar = false
		} else {
			if c == '*' {
				afterStar = true
			} else {
				writeRune(&buf, c)
			}
		}
	}

	if afterStar {
		buf.WriteString("[^/]*")
	}

	buf.WriteRune('$')
	return buf.String()
}

func writeRune(buf *bytes.Buffer, c rune) {
	if c == '.' || c == '+' || c == '^' || c == '[' || c == ']' || c == '{' || c == '}' {
		buf.WriteRune('\\')
	}
	buf.WriteRune(c)
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

	cfgFilters := pinpoint.GetConfig().StringSlice(CfgHttpServerExcludeUrl)
	trimStringSlice(cfgFilters)

	for _, u := range cfgFilters {
		filters = append(filters, newHttpExcludeUrl(u))
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
	cfg := pinpoint.GetConfig().StringSlice(CfgHttpServerExcludeMethod)
	trimStringSlice(cfg)

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
