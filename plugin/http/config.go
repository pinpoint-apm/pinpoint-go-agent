package http

import (
	"bytes"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

const (
	cfgHttpStatusCodeErrors    = "Http.StatusCodeErrors"
	cfgHttpExcludeUrl          = "Http.ExcludeUrl"
	cfgHttpExcludeMethod       = "Http.ExcludeMethod"
	cfgHttpRecordRequestHeader = "Http.RecordRequestHeader"
	cfgHttpRecordRespondHeader = "Http.RecordRespondHeader"
	cfgHttpRecordRequestCookie = "Http.RecordRequestCookie"
)

func init() {
	pinpoint.AddConfig(cfgHttpStatusCodeErrors, pinpoint.CfgStringSlice, []string{"5xx"})
	pinpoint.AddConfig(cfgHttpExcludeUrl, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpExcludeMethod, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpRecordRequestHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpRecordRespondHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpRecordRequestCookie, pinpoint.CfgStringSlice, []string{})
}

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

func newHttpStatusError() *httpStatusError {
	return &httpStatusError{
		errors: setupHttpStatusErrors(),
	}
}

func setupHttpStatusErrors() []httpStatusCode {
	var errors []httpStatusCode

	cfgErrors := pinpoint.GetConfig().StringSlice(cfgHttpStatusCodeErrors)
	trimStringSlice(cfgErrors)

	for _, s := range cfgErrors {
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

	cfgFilters := pinpoint.GetConfig().StringSlice(cfgHttpExcludeUrl)
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

type httpHeaderRecorder interface {
	recordHeader(annotation pinpoint.Annotation, aKey int, header http.Header)
	recordCookie(annotation pinpoint.Annotation, cookie []*http.Cookie)
}

type allHttpHeaderRecoder struct{}

func newAllHttpHeaderRecoder() *allHttpHeaderRecoder {
	return &allHttpHeaderRecoder{}
}

func (h *allHttpHeaderRecoder) recordHeader(annotation pinpoint.Annotation, key int, header http.Header) {
	for name, values := range header {
		vStr := strings.Join(values[:], ",")
		annotation.AppendStringString(int32(key), name, vStr)
	}
}

func (h *allHttpHeaderRecoder) recordCookie(annotation pinpoint.Annotation, cookie []*http.Cookie) {
	for _, c := range cookie {
		annotation.AppendStringString(pinpoint.AnnotationHttpCookie, c.Name, c.Value)
	}
}

type defaultHttpHeaderRecoder struct {
	cfg []string
}

func newDefaultHttpHeaderRecoder(headers []string) *defaultHttpHeaderRecoder {
	return &defaultHttpHeaderRecoder{cfg: headers}
}

func (h *defaultHttpHeaderRecoder) recordHeader(annotation pinpoint.Annotation, key int, header http.Header) {
	for _, name := range h.cfg {
		if v := header.Values(name); v != nil && len(v) > 0 {
			vStr := strings.Join(v[:], ",")
			annotation.AppendStringString(int32(key), name, vStr)
		}
	}
}

func (h *defaultHttpHeaderRecoder) recordCookie(annotation pinpoint.Annotation, cookie []*http.Cookie) {
	for _, name := range h.cfg {
		for _, c := range cookie {
			if strings.EqualFold(name, c.Name) {
				annotation.AppendStringString(pinpoint.AnnotationHttpCookie, c.Name, c.Value)
				break
			}
		}
	}
}

type noopHttpHeaderRecorder struct{}

func newNoopHttpHeaderRecorder() *noopHttpHeaderRecorder {
	return &noopHttpHeaderRecorder{}
}

func (h *noopHttpHeaderRecorder) recordHeader(annotation pinpoint.Annotation, key int, header http.Header) {
}

func (h *noopHttpHeaderRecorder) recordCookie(annotation pinpoint.Annotation, cookie []*http.Cookie) {
}

var (
	serverStatusErrors      *httpStatusError
	serverUrlFilter         *httpUrlFilter
	serverReqHeaderRecorder httpHeaderRecorder
	serverResHeaderRecorder httpHeaderRecorder
	srvCookieRecorder       httpHeaderRecorder
)

func httpStatusErrorHandler() *httpStatusError {
	if serverStatusErrors == nil {
		serverStatusErrors = newHttpStatusError()
	}

	return serverStatusErrors
}

func httpUrlFilterHandler() *httpUrlFilter {
	if serverUrlFilter == nil {
		serverUrlFilter = newHttpUrlFilter()
	}
	return serverUrlFilter
}

func serverRequestHeaderRecorder() httpHeaderRecorder {
	if serverReqHeaderRecorder == nil {
		cfgHeaders := pinpoint.GetConfig().StringSlice(cfgHttpRecordRequestHeader)
		trimStringSlice(cfgHeaders)
		serverReqHeaderRecorder = makeHttpHeaderRecoder(cfgHeaders)
	}
	return serverReqHeaderRecorder
}

func serverResponseHeaderRecorder() httpHeaderRecorder {
	if serverResHeaderRecorder == nil {
		cfgHeaders := pinpoint.GetConfig().StringSlice(cfgHttpRecordRequestHeader)
		trimStringSlice(cfgHeaders)
		serverResHeaderRecorder = makeHttpHeaderRecoder(cfgHeaders)
	}
	return serverResHeaderRecorder
}

func serverCookieRecorder() httpHeaderRecorder {
	if srvCookieRecorder == nil {
		cfgHeaders := pinpoint.GetConfig().StringSlice(cfgHttpRecordRequestCookie)
		trimStringSlice(cfgHeaders)
		srvCookieRecorder = makeHttpHeaderRecoder(cfgHeaders)
	}
	return srvCookieRecorder
}

func makeHttpHeaderRecoder(cfg []string) httpHeaderRecorder {
	if len(cfg) == 0 {
		return newNoopHttpHeaderRecorder()
	} else if strings.EqualFold(cfg[0], "HEADERS-ALL") {
		return newAllHttpHeaderRecoder()
	} else {
		return newDefaultHttpHeaderRecoder(cfg)
	}
}

func isHttpError(code int) bool {
	return httpStatusErrorHandler().isError(code)
}

func isExcludedUrl(url string) bool {
	return httpUrlFilterHandler().isFiltered(url)
}

func isExcludedMethod(method string) bool {
	cfgMethods := pinpoint.GetConfig().StringSlice(cfgHttpExcludeMethod)
	trimStringSlice(cfgMethods)

	for _, em := range cfgMethods {
		if strings.EqualFold(em, method) {
			return true
		}
	}
	return false
}

func serverHttpHeaderRecorder(key int) httpHeaderRecorder {
	if key == pinpoint.AnnotationHttpRequestHeader {
		return serverRequestHeaderRecorder()
	} else if key == pinpoint.AnnotationHttpResponseHeader {
		return serverResponseHeaderRecorder()
	} else if key == pinpoint.AnnotationHttpCookie {
		return serverCookieRecorder()
	} else {
		return newNoopHttpHeaderRecorder()
	}
}

func trimStringSlice(slice []string) {
	for i := range slice {
		slice[i] = strings.TrimSpace(slice[i])
	}
}

func WithHttpStatusCodeError(errors []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpStatusCodeErrors, errors)
	}
}

func WithHttpExcludeUrl(urlPath []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpExcludeUrl, urlPath)
	}
}

func WithHttpExcludeMethod(method []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpExcludeMethod, method)
	}
}

func WithHttpRecordRequestHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpRecordRequestHeader, header)
	}
}

func WithHttpRecordRespondHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpRecordRespondHeader, header)
	}
}

func WithHttpRecordRequestCookie(cookie []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpRecordRequestCookie, cookie)
	}
}

func recordHttpStatus(span pinpoint.SpanRecorder, status int) {
	span.Annotations().AppendInt(pinpoint.AnnotationHttpStatusCode, int32(status))
	if isHttpError(status) {
		span.SetFailure()
	}
}

func recordHttpHeader(annotation pinpoint.Annotation, key int, header http.Header) {
	serverHttpHeaderRecorder(key).recordHeader(annotation, key, header)
}

func recordHttpCookie(annotation pinpoint.Annotation, cookie []*http.Cookie) {
	serverHttpHeaderRecorder(pinpoint.AnnotationHttpCookie).recordCookie(annotation, cookie)

}
