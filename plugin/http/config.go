package pphttp

import (
	"net/textproto"
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
	CfgHttpServerProxyUserHeaderNames = "Http.Server.ProxyUserHeaderNames"
	CfgHttpServerProxyHeaderEnable    = "Http.Server.ProxyHeaderEnable"
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
	pinpoint.AddConfig(CfgHttpServerProxyUserHeaderNames, pinpoint.CfgStringSlice, []string{}, true)
	pinpoint.AddConfig(CfgHttpServerProxyHeaderEnable, pinpoint.CfgBool, true, true)
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

// WithHttpServerProxyHeaderEnable turns the recording of proxy request headers
// (Pinpoint-ProxyApache, -ProxyNginx, -ProxyApp and the configured user
// headers) on or off.
func WithHttpServerProxyHeaderEnable(enable bool) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerProxyHeaderEnable, enable)
	}
}

// WithHttpServerProxyUserHeaderNames sets the request headers a user-defined
// proxy writes its receive time into ("t=<epoch millis>"). Each one present on
// a request is recorded as a proxy annotation of type USER (4), with the header
//
//	pphttp.WithHttpServerProxyUserHeaderNames([]string{"X-Proxy-Time"})
func WithHttpServerProxyUserHeaderNames(names []string) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgHttpServerProxyUserHeaderNames, names)
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
	// Pre-canonicalized proxy user header names; see proxyHeaderApache.
	srvProxyUserHeaders []string
	srvProxyHeader      bool
}

var httpConfigOpts = []string{
	CfgHttpServerStatusCodeErrors,
	CfgHttpServerExcludeUrl,
	CfgHttpServerExcludeMethod,
	CfgHttpServerRecordRequestHeader,
	CfgHttpServerRecordResponseHeader,
	CfgHttpServerRecordRequestCookie,
	CfgHttpServerRecordHandlerError,
	CfgHttpServerProxyUserHeaderNames,
	CfgHttpServerProxyHeaderEnable,
	CfgHttpClientRecordRequestHeader,
	CfgHttpClientRecordResponseHeader,
	CfgHttpClientRecordRequestCookie,
	pinpoint.CfgHttpUrlStatEnable,
}

var (
	httpConfigMu     sync.Mutex
	curHttpConfig    atomic.Pointer[httpConfig]
	httpConfigSource atomic.Pointer[httpConfigOwner]
)

type httpConfigOwner struct{ agent pinpoint.Agent }

// httpCfg returns the config derived from the current agent. A new agent gets
// its own derived value and reload callback, so restarting in the same process
// cannot retain the previous agent's filters and recorders.
//
// ponytail: this store and the agent's config snapshot are two separate
// publications, so a reload lands in two steps. Nothing couples them (each is
// internally consistent on its own), and folding these into the agent snapshot
// would mean either an import cycle or a map[string]any registry with a type
// assertion on every request. Revisit if a derived value ever has to agree with
// an agent option within the same generation.
func httpCfg() *httpConfig {
	agent := pinpoint.GetAgent()
	if source := httpConfigSource.Load(); source != nil && source.agent == agent {
		return curHttpConfig.Load()
	}

	httpConfigMu.Lock()
	defer httpConfigMu.Unlock()
	if source := httpConfigSource.Load(); source == nil || source.agent != agent {
		config := agent.Config()
		curHttpConfig.Store(newHttpConfigFor(config))
		httpConfigSource.Store(&httpConfigOwner{agent: agent})
		config.AddReloadCallback(httpConfigOpts, func() {
			httpConfigMu.Lock()
			defer httpConfigMu.Unlock()
			if source := httpConfigSource.Load(); source != nil && source.agent == agent {
				curHttpConfig.Store(newHttpConfigFor(config))
			}
		})
	}
	return curHttpConfig.Load()
}

func newHttpConfig() *httpConfig {
	return newHttpConfigFor(pinpoint.GetConfig())
}

func newHttpConfigFor(config *pinpoint.Config) *httpConfig {
	return &httpConfig{
		srvUrl:              setupHttpUrlFilter(trimStringSlice(config.StringSlice(CfgHttpServerExcludeUrl))),
		srvMethod:           &httpMethodFilter{excludeMethod: trimStringSlice(config.StringSlice(CfgHttpServerExcludeMethod))},
		srvStatus:           parseHttpStatusErrors(config.StringSlice(CfgHttpServerStatusCodeErrors)),
		srvReqHeader:        makeHttpHeaderRecorderFor(config, CfgHttpServerRecordRequestHeader),
		srvResHeader:        makeHttpHeaderRecorderFor(config, CfgHttpServerRecordResponseHeader),
		srvCookie:           makeHttpHeaderRecorderFor(config, CfgHttpServerRecordRequestCookie),
		cltReqHeader:        makeHttpHeaderRecorderFor(config, CfgHttpClientRecordRequestHeader),
		cltResHeader:        makeHttpHeaderRecorderFor(config, CfgHttpClientRecordResponseHeader),
		cltCookie:           makeHttpHeaderRecorderFor(config, CfgHttpClientRecordRequestCookie),
		recordHandlerError:  config.Bool(CfgHttpServerRecordHandlerError),
		urlStatEnabled:      config.Bool(pinpoint.CfgHttpUrlStatEnable),
		srvProxyUserHeaders: makeProxyUserHeaderNames(config.StringSlice(CfgHttpServerProxyUserHeaderNames)),
		srvProxyHeader:      config.Bool(CfgHttpServerProxyHeaderEnable),
	}
}

func makeProxyUserHeaderNames(cfg []string) []string {
	var names []string
	for _, name := range trimStringSlice(cfg) {
		if name != "" {
			names = append(names, textproto.CanonicalMIMEHeaderKey(name))
		}
	}
	return names
}

func proxyUserHeaderNames() []string {
	return httpCfg().srvProxyUserHeaders
}

func proxyHeaderEnabled() bool {
	return httpCfg().srvProxyHeader
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
		// records ErrorCategory.HTTP_STATUS: an operator who does not want a
		// 5xx to count as a transaction failure drops that one cause with
		// Span.ErrorMarkExclude and keeps every other kind of failure. The
		// annotation below is recorded either way.
		span.SetFailure(pinpoint.ErrorCategoryHttpStatus)
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
	if httpCfg().recordHandlerError {
		tracer.Span().SetError(err)
	}
}

func makeHttpHeaderRecorder(cfgName string) httpHeaderRecorder {
	return makeHttpHeaderRecorderFor(pinpoint.GetConfig(), cfgName)
}

func makeHttpHeaderRecorderFor(config *pinpoint.Config, cfgName string) httpHeaderRecorder {
	cfg := trimStringSlice(config.StringSlice(cfgName))

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
