package http

import (
	"net/http"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

const (
	cfgHttpServerStatusCodeErrors     = "Http.Server.StatusCodeErrors"
	cfgHttpServerExcludeUrl           = "Http.Server.ExcludeUrl"
	cfgHttpServerExcludeMethod        = "Http.Server.ExcludeMethod"
	cfgHttpServerRecordRequestHeader  = "Http.Server.RecordRequestHeader"
	cfgHttpServerRecordResponseHeader = "Http.Server.RecordResponseHeader"
	cfgHttpServerRecordRequestCookie  = "Http.Server.RecordRequestCookie"
	cfgHttpClientRecordRequestHeader  = "Http.Client.RecordRequestHeader"
	cfgHttpClientRecordResponseHeader = "Http.Client.RecordResponseHeader"
	cfgHttpClientRecordRequestCookie  = "Http.Client.RecordRequestCookie"
)

func init() {
	pinpoint.AddConfig(cfgHttpServerStatusCodeErrors, pinpoint.CfgStringSlice, []string{"5xx"})
	pinpoint.AddConfig(cfgHttpServerExcludeUrl, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpServerExcludeMethod, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpServerRecordRequestHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpServerRecordResponseHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpServerRecordRequestCookie, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpClientRecordRequestHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpClientRecordResponseHeader, pinpoint.CfgStringSlice, []string{})
	pinpoint.AddConfig(cfgHttpClientRecordRequestCookie, pinpoint.CfgStringSlice, []string{})
}

func WithHttpServerStatusCodeError(errors []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpServerStatusCodeErrors, errors)
	}
}

func WithHttpServerExcludeUrl(urlPath []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpServerExcludeUrl, urlPath)
	}
}

func WithHttpServerExcludeMethod(method []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpServerExcludeMethod, method)
	}
}

func WithHttpServerRecordRequestHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpServerRecordRequestHeader, header)
	}
}

func WithHttpServerRecordRespondHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpServerRecordResponseHeader, header)
	}
}

func WithHttpServerRecordRequestCookie(cookie []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpServerRecordRequestCookie, cookie)
	}
}

func WithHttpClientRecordRequestHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpClientRecordRequestHeader, header)
	}
}

func WithHttpClientRecordRespondHeader(header []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpClientRecordResponseHeader, header)
	}
}

func WithHttpClientRecordRequestCookie(cookie []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(cfgHttpClientRecordRequestCookie, cookie)
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
		srvReqHeaderRecorder = makeHttpHeaderRecorder(cfgHttpServerRecordRequestHeader)
	}
	return srvReqHeaderRecorder
}

func serverResponseHeaderRecorder() httpHeaderRecorder {
	if srvResHeaderRecorder == nil {
		srvResHeaderRecorder = makeHttpHeaderRecorder(cfgHttpServerRecordResponseHeader)
	}
	return srvResHeaderRecorder
}

func serverCookieRecorder() httpHeaderRecorder {
	if srvCookieRecorder == nil {
		srvCookieRecorder = makeHttpHeaderRecorder(cfgHttpServerRecordRequestCookie)
	}
	return srvCookieRecorder
}

func clientRequestHeaderRecorder() httpHeaderRecorder {
	if cltReqHeaderRecorder == nil {
		cltReqHeaderRecorder = makeHttpHeaderRecorder(cfgHttpClientRecordRequestHeader)
	}
	return cltReqHeaderRecorder
}

func clientResponseHeaderRecorder() httpHeaderRecorder {
	if cltResHeaderRecorder == nil {
		cltResHeaderRecorder = makeHttpHeaderRecorder(cfgHttpClientRecordResponseHeader)
	}
	return cltResHeaderRecorder
}

func clientCookieRecorder() httpHeaderRecorder {
	if cltCookieRecorder == nil {
		cltCookieRecorder = makeHttpHeaderRecorder(cfgHttpClientRecordRequestCookie)
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
