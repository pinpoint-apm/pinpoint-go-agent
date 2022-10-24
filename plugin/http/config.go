package pphttp

import (
	"strings"
	"sync"

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
	onceUrl sync.Once
	srvUrl  *httpUrlFilter
)

func isExcludedUrl(url string) bool {
	onceUrl.Do(func() {
		srvUrl = newHttpUrlFilter()
	})
	return srvUrl.isFiltered(url)
}

var (
	onceMethod sync.Once
	srvMethod  *httpMethodFilter
)

func isExcludedMethod(method string) bool {
	onceMethod.Do(func() {
		srvMethod = newHttpExcludeMethod()
	})
	return srvMethod.isExcludedMethod(method)
}

var (
	onceStatus sync.Once
	srvStatus  *httpStatusError
)

func recordServerHttpStatus(span pinpoint.SpanRecorder, status int) {
	onceStatus.Do(func() {
		srvStatus = newHttpStatusError()
	})
	if srvStatus.isError(status) {
		span.SetFailure()
	}
	span.Annotations().AppendInt(pinpoint.AnnotationHttpStatusCode, int32(status))
}

var (
	onceSrvReq   sync.Once
	srvReqHeader httpHeaderRecorder
)

func recordServerHttpRequestHeader(annotation pinpoint.Annotation, header Header) {
	onceSrvReq.Do(func() {
		srvReqHeader = makeHttpHeaderRecorder(CfgHttpServerRecordRequestHeader)
	})
	srvReqHeader.recordHeader(annotation, pinpoint.AnnotationHttpRequestHeader, header)
}

var (
	onceSrvRes   sync.Once
	srvResHeader httpHeaderRecorder
)

func recordServerHttpResponseHeader(annotation pinpoint.Annotation, header Header) {
	onceSrvRes.Do(func() {
		srvResHeader = makeHttpHeaderRecorder(CfgHttpServerRecordResponseHeader)
	})
	srvResHeader.recordHeader(annotation, pinpoint.AnnotationHttpResponseHeader, header)
}

var (
	onceSrvCookie sync.Once
	srvCookie     httpHeaderRecorder
)

func recordServerHttpCookie(annotation pinpoint.Annotation, cookie Cookie) {
	onceSrvCookie.Do(func() {
		srvCookie = makeHttpHeaderRecorder(CfgHttpServerRecordRequestCookie)
	})
	srvCookie.recordCookie(annotation, cookie)
}

var (
	onceCltReq   sync.Once
	cltReqHeader httpHeaderRecorder
)

func RecordClientHttpRequestHeader(annotation pinpoint.Annotation, header Header) {
	onceCltReq.Do(func() {
		cltReqHeader = makeHttpHeaderRecorder(CfgHttpClientRecordRequestHeader)
	})
	cltReqHeader.recordHeader(annotation, pinpoint.AnnotationHttpRequestHeader, header)
}

var (
	onceCltRes   sync.Once
	cltResHeader httpHeaderRecorder
)

func RecordClientHttpResponseHeader(annotation pinpoint.Annotation, header Header) {
	onceCltRes.Do(func() {
		cltResHeader = makeHttpHeaderRecorder(CfgHttpClientRecordResponseHeader)
	})
	cltResHeader.recordHeader(annotation, pinpoint.AnnotationHttpResponseHeader, header)
}

var (
	onceCltCookie sync.Once
	cltCookie     httpHeaderRecorder
)

func RecordClientHttpCookie(annotation pinpoint.Annotation, cookie Cookie) {
	onceCltCookie.Do(func() {
		cltCookie = makeHttpHeaderRecorder(CfgHttpClientRecordRequestCookie)
	})
	cltCookie.recordCookie(annotation, cookie)
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
