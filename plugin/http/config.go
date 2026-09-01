package pphttp

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

const (
	CfgHttpServerStatusCodeErrors     = "Http.Server.StatusCodeErrors"
	CfgHttpServerExcludeUrl           = "Http.Server.ExcludeUrl"
	CfgHttpServerExcludeMethod        = "Http.Server.ExcludeMethod"
	CfgHttpServerRecordRequestHeader  = "Http.Server.RecordRequestHeader"
	CfgHttpServerRecordResponseHeader = "Http.Server.RecordResponseHeader"
	CfgHttpServerRecordRequestCookie  = "Http.Server.RecordRequestCookie"
	CfgHttpServerRecordHandlerError   = "Http.Server.RecordHandlerError"
	CfgHttpClientRecordRequestHeader  = "Http.Client.RecordRequestHeader"
	CfgHttpClientRecordResponseHeader = "Http.Client.RecordResponseHeader"
	CfgHttpClientRecordRequestCookie  = "Http.Client.RecordRequestCookie"
)

func init() {
	pinpoint.AddConfig(CfgHttpServerStatusCodeErrors, pinpoint.CfgStringSlice, []string{"5xx"}, true)
	pinpoint.AddConfig(CfgHttpServerExcludeUrl, pinpoint.CfgStringSlice, []string{}, true)
	pinpoint.AddConfig(CfgHttpServerExcludeMethod, pinpoint.CfgStringSlice, []string{}, true)
	pinpoint.AddConfig(CfgHttpServerRecordRequestHeader, pinpoint.CfgStringSlice, []string{}, true)
	pinpoint.AddConfig(CfgHttpServerRecordResponseHeader, pinpoint.CfgStringSlice, []string{}, true)
	pinpoint.AddConfig(CfgHttpServerRecordRequestCookie, pinpoint.CfgStringSlice, []string{}, true)
	pinpoint.AddConfig(CfgHttpServerRecordHandlerError, pinpoint.CfgBool, true, true)
	pinpoint.AddConfig(CfgHttpClientRecordRequestHeader, pinpoint.CfgStringSlice, []string{}, true)
	pinpoint.AddConfig(CfgHttpClientRecordResponseHeader, pinpoint.CfgStringSlice, []string{}, true)
	pinpoint.AddConfig(CfgHttpClientRecordRequestCookie, pinpoint.CfgStringSlice, []string{}, true)
}

// WithHttpServerStatusCodeError sets HTTP status code with request failure.
//
//	pphttp.WithHttpServerStatusCodeError([]string{"5xx", "4xx", "302"})
func WithHttpServerStatusCodeError(errors []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerStatusCodeErrors, errors)
	}
}

// WithHttpServerRecordHandlerError sets whether to record the error returned by http handler.
//
//	pphttp.WithHttpServerRecordHandlerError(false)
func WithHttpServerRecordHandlerError(record bool) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerRecordHandlerError, record)
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

// httpConfig bundles every component derived from this plugin's options. A
// reload rebuilds it whole and publishes it with a single atomic store, so a
// request never sees a partially initialized filter or recorder.
type httpConfig struct {
	srvUrl             *httpUrlFilter
	srvMethod          *httpMethodFilter
	srvStatus          *httpStatusError
	srvReqHeader       httpHeaderRecorder
	srvResHeader       httpHeaderRecorder
	srvCookie          httpHeaderRecorder
	cltReqHeader       httpHeaderRecorder
	cltResHeader       httpHeaderRecorder
	cltCookie          httpHeaderRecorder
	recordHandlerError bool
	urlStatEnabled     bool
}

var httpConfigOpts = []string{
	CfgHttpServerStatusCodeErrors,
	CfgHttpServerExcludeUrl,
	CfgHttpServerExcludeMethod,
	CfgHttpServerRecordRequestHeader,
	CfgHttpServerRecordResponseHeader,
	CfgHttpServerRecordRequestCookie,
	CfgHttpServerRecordHandlerError,
	CfgHttpClientRecordRequestHeader,
	CfgHttpClientRecordResponseHeader,
	CfgHttpClientRecordRequestCookie,
	pinpoint.CfgHttpUrlStatEnable,
}

var (
	onceHttpConfig sync.Once
	curHttpConfig  atomic.Pointer[httpConfig]
)

// httpCfg returns the published config. The first call builds it - the agent
// config does not exist yet at package init time - and registers the reload
// callback that republishes it.
//
// ponytail: this store and the agent's config snapshot are two separate
// publications, so a reload lands in two steps. Nothing couples them (each is
// internally consistent on its own), and folding these into the agent snapshot
// would mean either an import cycle or a map[string]any registry with a type
// assertion on every request. Revisit if a derived value ever has to agree with
// an agent option within the same generation.
func httpCfg() *httpConfig {
	onceHttpConfig.Do(func() {
		curHttpConfig.Store(newHttpConfig())
		pinpoint.GetConfig().AddReloadCallback(httpConfigOpts, func() {
			curHttpConfig.Store(newHttpConfig())
		})
	})
	return curHttpConfig.Load()
}

func newHttpConfig() *httpConfig {
	return &httpConfig{
		srvUrl:             newHttpUrlFilter(),
		srvMethod:          newHttpExcludeMethod(),
		srvStatus:          newHttpStatusError(),
		srvReqHeader:       makeHttpHeaderRecorder(CfgHttpServerRecordRequestHeader),
		srvResHeader:       makeHttpHeaderRecorder(CfgHttpServerRecordResponseHeader),
		srvCookie:          makeHttpHeaderRecorder(CfgHttpServerRecordRequestCookie),
		cltReqHeader:       makeHttpHeaderRecorder(CfgHttpClientRecordRequestHeader),
		cltResHeader:       makeHttpHeaderRecorder(CfgHttpClientRecordResponseHeader),
		cltCookie:          makeHttpHeaderRecorder(CfgHttpClientRecordRequestCookie),
		recordHandlerError: pinpoint.GetConfig().Bool(CfgHttpServerRecordHandlerError),
		urlStatEnabled:     pinpoint.GetConfig().Bool(pinpoint.CfgHttpUrlStatEnable),
	}
}

// IsUrlStatEnabled reports whether URL statistics collection is enabled.
// Plugins whose route pattern is expensive to look up (a context walk, a lock,
// a string build) use it to skip that lookup when CollectUrlStat would drop
// the entry anyway.
func IsUrlStatEnabled() bool {
	return httpCfg().urlStatEnabled
}

func isExcludedUrl(url string) bool {
	return httpCfg().srvUrl.isFiltered(url)
}

func isExcludedMethod(method string) bool {
	return httpCfg().srvMethod.isExcludedMethod(method)
}

func recordServerHttpStatus(span pinpoint.SpanRecorder, status int) {
	if httpCfg().srvStatus.isError(status) {
		span.SetFailure()
	}
	span.Annotations().AppendInt(pinpoint.AnnotationHttpStatusCode, int32(status))
}

func recordServerHttpRequestHeader(annotation pinpoint.Annotation, header Header) {
	httpCfg().srvReqHeader.recordHeader(annotation, pinpoint.AnnotationHttpRequestHeader, header)
}

func recordServerHttpResponseHeader(annotation pinpoint.Annotation, header Header) {
	httpCfg().srvResHeader.recordHeader(annotation, pinpoint.AnnotationHttpResponseHeader, header)
}

func recordServerHttpCookie(annotation pinpoint.Annotation, cookie Cookie) {
	httpCfg().srvCookie.recordCookie(annotation, cookie)
}

func RecordClientHttpRequestHeader(annotation pinpoint.Annotation, header Header) {
	httpCfg().cltReqHeader.recordHeader(annotation, pinpoint.AnnotationHttpRequestHeader, header)
}

func RecordClientHttpResponseHeader(annotation pinpoint.Annotation, header Header) {
	httpCfg().cltResHeader.recordHeader(annotation, pinpoint.AnnotationHttpResponseHeader, header)
}

func RecordClientHttpCookie(annotation pinpoint.Annotation, cookie Cookie) {
	httpCfg().cltCookie.recordCookie(annotation, cookie)
}

// RecordHttpHandlerError records error returned by http handler.
func RecordHttpHandlerError(tracer pinpoint.Tracer, err error) {
	if tracer.IsSampled() && httpCfg().recordHandlerError {
		tracer.Span().SetError(err)
	}
}

func makeHttpHeaderRecorder(cfgName string) httpHeaderRecorder {
	cfg := trimStringSlice(pinpoint.GetConfig().StringSlice(cfgName))

	if len(cfg) == 0 {
		return newNoopHttpHeaderRecorder()
	} else if strings.EqualFold(cfg[0], "HEADERS-ALL") {
		return newAllHttpHeaderRecorder()
	} else {
		return newDefaultHttpHeaderRecorder(cfg)
	}
}

// trimStringSlice returns a trimmed copy: the slice handed out by StringSlice
// belongs to the published config snapshot and must not be written to.
func trimStringSlice(slice []string) []string {
	trimmed := make([]string, len(slice))
	for i, s := range slice {
		trimmed[i] = strings.TrimSpace(s)
	}
	return trimmed
}
