package pphttp

import (
	"net/http"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

const (
	CfgHttpServerStatusCodeErrors     = "Http.Server.StatusCodeErrors"
	CfgHttpServerExcludeUrl           = "Http.Server.ExcludeUrl"
	CfgHttpServerExcludeMethod        = "Http.Server.ExcludeMethod"
	CfgHttpServerRecordRequestHeader  = "Http.Server.RecordRequestHeader"
	CfgHttpServerRecordResponseHeader = "Http.Server.RecordResponseHeader"
	CfgHttpServerRecordRequestCookie  = "Http.Server.RecordRequestCookie"
	CfgHttpClientRecordRequestHeader  = "Http.Client.RecordRequestHeader"
	CfgHttpClientRecordResponseHeader = "Http.Client.RecordResponseHeader"
	CfgHttpClientRecordRequestCookie  = "Http.Client.RecordRequestCookie"
)

func init() {
	pinpoint.AddConfig(CfgHttpServerStatusCodeErrors, pinpoint.CfgStringSlice, []string{"5xx"})
	pinpoint.AddConfig(CfgHttpServerExcludeUrl, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(CfgHttpServerExcludeMethod, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(CfgHttpServerRecordRequestHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(CfgHttpServerRecordResponseHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(CfgHttpServerRecordRequestCookie, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(CfgHttpClientRecordRequestHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(CfgHttpClientRecordResponseHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(CfgHttpClientRecordRequestCookie, pinpoint.CfgStringSlice, []string{})
}

// WithHttpServerStatusCodeError sets HTTP status code with request failure.
//
//	pphttp.WithHttpServerStatusCodeError([]string{"5xx", "4xx", "302"})
func WithHttpServerStatusCodeError(errors []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerStatusCodeErrors, errors)
	}
}

// WithHttpServerExcludeUrl sets URLs to exclude from tracking.
// It supports ant style pattern. e.g. /aa/*.html, /??/exclude.html
//
//	pphttp.WithHttpServerExcludeUrl([]string{"/wrap_*", "/**/*.do"})
func WithHttpServerExcludeUrl(urlPath []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerExcludeUrl, urlPath)
	}
}

// WithHttpServerExcludeMethod sets HTTP Request methods to exclude from tracking.
//
//	pphttp.WithHttpServerExcludeMethod([]string{"put", "delete"})
func WithHttpServerExcludeMethod(method []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerExcludeMethod, method)
	}
}

// WithHttpServerRecordRequestHeader sets HTTP request headers to be logged on the server side.
// If sets to HEADERS-ALL, it records all request headers.
//
//	pphttp.WithHttpServerRecordRequestHeader([]string{"HEADERS-ALL"})
//
// or
//
//	pphttp.WithHttpServerRecordRequestHeader([]string{"foo", "bar"})
func WithHttpServerRecordRequestHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerRecordRequestHeader, header)
	}
}

// WithHttpServerRecordRespondHeader sets HTTP response headers to be logged on the server side.
// If sets to HEADERS-ALL, it records all response headers.
//
//	pphttp.WithHttpServerRecordRespondHeader([]string{"HEADERS-ALL"})
//
// or
//
//	pphttp.WithHttpServerRecordRespondHeader([]string{"foo", "bar", "set-cookie"})
func WithHttpServerRecordRespondHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerRecordResponseHeader, header)
	}
}

// WithHttpServerRecordRequestCookie sets HTTP request cookies to be logged on the server side.
// If sets to HEADERS-ALL, it records all request cookies.
//
//	pphttp.WithHttpServerRecordRequestCookie([]string{"HEADERS-ALL"})
//
// or
//
//	pphttp.WithHttpServerRecordRequestCookie([]string{"foo", "bar"})
func WithHttpServerRecordRequestCookie(cookie []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerRecordRequestCookie, cookie)
	}
}

// WithHttpClientRecordRequestHeader sets HTTP request headers to be logged on the client side.
// If sets to HEADERS-ALL, it records all request headers.
//
//	pphttp.WithHttpClientRecordRequestHeader([]string{"HEADERS-ALL"})
//
// or
//
//	pphttp.WithHttpClientRecordRequestHeader([]string{"foo", "bar"})
func WithHttpClientRecordRequestHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpClientRecordRequestHeader, header)
	}
}

// WithHttpClientRecordRespondHeader sets HTTP response headers to be logged on the client side.
// If sets to HEADERS-ALL, it records all response headers.
//
//	pphttp.WithHttpClientRecordRespondHeader([]string{"HEADERS-ALL"})
//
// or
//
//	pphttp.WithHttpClientRecordRespondHeader([]string{"foo", "bar"})
func WithHttpClientRecordRespondHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpClientRecordResponseHeader, header)
	}
}

// WithHttpClientRecordRequestCookie sets HTTP request cookies to be logged on the client side.
// If sets to HEADERS-ALL, it records all request cookies.
//
//	pphttp.WithHttpClientRecordRequestCookie([]string{"HEADERS-ALL"})
//
// or
//
//	pphttp.WithHttpClientRecordRequestCookie([]string{"foo", "bar"})
func WithHttpClientRecordRequestCookie(cookie []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpClientRecordRequestCookie, cookie)
	}
}

var (
	srvStatusErrors *httpStatusError
	srvUrlFilter    *httpUrlFilter
	srvMethodFilter *httpMethodFilter

	srvReqHeaderRecorder httpHeaderRecorder
	srvResHeaderRecorder httpHeaderRecorder
	srvCookieRecorder    httpHeaderRecorder

	cltReqHeaderRecorder httpHeaderRecorder
	cltResHeaderRecorder httpHeaderRecorder
	cltCookieRecorder    httpHeaderRecorder
)

func serverStatusError() *httpStatusError {
	if srvStatusErrors == nil {
		srvStatusErrors = newHttpStatusError()
	}

	return srvStatusErrors
}

func serverUrlFilter() *httpUrlFilter {
	if srvUrlFilter == nil {
		srvUrlFilter = newHttpUrlFilter()
	}
	return srvUrlFilter
}

func serverMethodFilter() *httpMethodFilter {
	if srvMethodFilter == nil {
		srvMethodFilter = newHttpExcludeMethod()
	}
	return srvMethodFilter
}

func serverRequestHeaderRecorder() httpHeaderRecorder {
	if srvReqHeaderRecorder == nil {
		srvReqHeaderRecorder = makeHttpHeaderRecorder(CfgHttpServerRecordRequestHeader)
	}
	return srvReqHeaderRecorder
}

func serverResponseHeaderRecorder() httpHeaderRecorder {
	if srvResHeaderRecorder == nil {
		srvResHeaderRecorder = makeHttpHeaderRecorder(CfgHttpServerRecordResponseHeader)
	}
	return srvResHeaderRecorder
}

func serverCookieRecorder() httpHeaderRecorder {
	if srvCookieRecorder == nil {
		srvCookieRecorder = makeHttpHeaderRecorder(CfgHttpServerRecordRequestCookie)
	}
	return srvCookieRecorder
}

func clientRequestHeaderRecorder() httpHeaderRecorder {
	if cltReqHeaderRecorder == nil {
		cltReqHeaderRecorder = makeHttpHeaderRecorder(CfgHttpClientRecordRequestHeader)
	}
	return cltReqHeaderRecorder
}

func clientResponseHeaderRecorder() httpHeaderRecorder {
	if cltResHeaderRecorder == nil {
		cltResHeaderRecorder = makeHttpHeaderRecorder(CfgHttpClientRecordResponseHeader)
	}
	return cltResHeaderRecorder
}

func clientCookieRecorder() httpHeaderRecorder {
	if cltCookieRecorder == nil {
		cltCookieRecorder = makeHttpHeaderRecorder(CfgHttpClientRecordRequestCookie)
	}
	return cltCookieRecorder
}

func makeHttpHeaderRecorder(cfgName string) httpHeaderRecorder {
	cfg := pinpoint.GetConfig().StringSlice(cfgName)
	trimStringSlice(cfg)

	if len(cfg) == 0 {
		return newNoopHttpHeaderRecorder()
	} else if strings.EqualFold(cfg[0], "HEADERS-ALL") {
		return newAllHttpHeaderRecorder()
	} else {
		return newDefaultHttpHeaderRecorder(cfg)
	}
}

func trimStringSlice(slice []string) {
	for i := range slice {
		slice[i] = strings.TrimSpace(slice[i])
	}
}

func isExcludedUrl(url string) bool {
	return serverUrlFilter().isFiltered(url)
}

func isExcludedMethod(method string) bool {
	return serverMethodFilter().isExcludedMethod(method)
}

func recordServerHttpStatus(span pinpoint.SpanRecorder, status int) {
	span.Annotations().AppendInt(pinpoint.AnnotationHttpStatusCode, int32(status))
	if serverStatusError().isError(status) {
		span.SetFailure()
	}
}

func recordServerHttpRequestHeader(annotation pinpoint.Annotation, header http.Header) {
	serverRequestHeaderRecorder().recordHeader(annotation, pinpoint.AnnotationHttpRequestHeader, header)
}

func recordServerHttpResponseHeader(annotation pinpoint.Annotation, header http.Header) {
	serverResponseHeaderRecorder().recordHeader(annotation, pinpoint.AnnotationHttpResponseHeader, header)
}

func recordServerHttpCookie(annotation pinpoint.Annotation, cookie []*http.Cookie) {
	serverCookieRecorder().recordCookie(annotation, cookie)
}

func recordClientHttpRequestHeader(annotation pinpoint.Annotation, header http.Header) {
	clientRequestHeaderRecorder().recordHeader(annotation, pinpoint.AnnotationHttpRequestHeader, header)
}

func recordClientHttpResponseHeader(annotation pinpoint.Annotation, header http.Header) {
	clientResponseHeaderRecorder().recordHeader(annotation, pinpoint.AnnotationHttpResponseHeader, header)
}

func recordClientHttpCookie(annotation pinpoint.Annotation, cookie []*http.Cookie) {
	clientCookieRecorder().recordCookie(annotation, cookie)
}
