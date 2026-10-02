package pphttp

import (
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// Shorthands for the config the request path reads: newHttpConfig builds it
// from the agent config, the others read the one httpCfg holds.
func newHttpConfig() *httpConfig { return newHttpConfigFor(pinpoint.GetConfig()) }

func isExcludedUrl(url string) bool { return httpCfg().srvUrl.isFiltered(url) }

func isExcludedMethod(method string) bool { return httpCfg().srvMethod.isExcludedMethod(method) }

func makeHttpHeaderRecorder(cfgName string) httpHeaderRecorder {
	return makeHttpHeaderRecorderFor(pinpoint.GetConfig(), cfgName)
}

func setProxyHeader(a pinpoint.Annotation, h Header) {
	setProxyHeaderNames(a, h, httpCfg().srvProxyUserHeaders)
}
