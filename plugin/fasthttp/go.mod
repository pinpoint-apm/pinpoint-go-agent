module github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.4.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.4.0
	github.com/valyala/fasthttp v1.40.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
