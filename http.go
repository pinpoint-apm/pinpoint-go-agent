package pinpoint

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

type httpStatusCode interface {
	isError(code int) bool
}

type httpStatusInformational struct{}

func newHttpStatusInformational() *httpStatusInformational {
	return &httpStatusInformational{}
}

func (h *httpStatusInformational) isError(code int) bool {
	return 100 <= code && code <= 199
}

type httpStatusSuccess struct{}

func newHttpStatusSuccess() *httpStatusSuccess {
	return &httpStatusSuccess{}
}

func (h *httpStatusSuccess) isError(code int) bool {
	return 200 <= code && code <= 299
}

type httpStatusRedirection struct{}

func newHttpStatusRedirection() *httpStatusRedirection {
	return &httpStatusRedirection{}
}

func (h *httpStatusRedirection) isError(code int) bool {
	return 300 <= code && code <= 399
}

type httpStatusClientError struct{}

func newHttpStatusClientError() *httpStatusClientError {
	return &httpStatusClientError{}
}

func (h *httpStatusClientError) isError(code int) bool {
	return 400 <= code && code <= 499
}

type httpStatusServerError struct{}

func newHttpStatusServerError() *httpStatusServerError {
	return &httpStatusServerError{}
}

func (h *httpStatusServerError) isError(code int) bool {
	return 500 <= code && code <= 599
}

type httpStatusDefault struct {
	statusCode int
}

func newHttpStatusDefault(code int) *httpStatusDefault {
	return &httpStatusDefault{
		statusCode: code,
	}
}

func (h *httpStatusDefault) isError(code int) bool {
	return h.statusCode == code
}

type httpStatusError struct {
	errors []httpStatusCode
}

func newHttpStatusError(config *Config) *httpStatusError {
	return &httpStatusError{
		errors: setupHttpStatusErrors(config),
	}
}

func setupHttpStatusErrors(config *Config) []httpStatusCode {
	var errors []httpStatusCode

	for _, s := range config.Http.StatusCodeErrors {
		if strings.EqualFold(s, "5xx") {
			errors = append(errors, newHttpStatusServerError())
		} else if strings.EqualFold(s, "4xx") {
			errors = append(errors, newHttpStatusClientError())
		} else if strings.EqualFold(s, "3xx") {
			errors = append(errors, newHttpStatusRedirection())
		} else if strings.EqualFold(s, "2xx") {
			errors = append(errors, newHttpStatusSuccess())
		} else if strings.EqualFold(s, "1xx") {
			errors = append(errors, newHttpStatusInformational())
		} else {
			c, e := strconv.Atoi(s)
			if e != nil {
				c = -1
			}
			errors = append(errors, newHttpStatusDefault(c))
		}
	}

	return errors
}

func (h *httpStatusError) isError(code int) bool {
	for _, h := range h.errors {
		if h.isError(code) {
			return true
		}
	}
	return false
}

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

	log("agent").Debug("newHttpExcludeUrl: ", urlPath, convertToRegexp(urlPath))

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

func newHttpUrlFilter(config *Config) *httpUrlFilter {
	return &httpUrlFilter{
		filters: setupHttpUrlFilter(config),
	}
}

func setupHttpUrlFilter(config *Config) []*httpExcludeUrl {
	var filters []*httpExcludeUrl

	for _, u := range config.Http.ExcludeUrl {
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
