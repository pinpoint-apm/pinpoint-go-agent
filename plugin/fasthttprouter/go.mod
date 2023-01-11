module github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter

go 1.15

require (
	github.com/fasthttp/router v1.4.14
	github.com/pinpoint-apm/pinpoint-go-agent v1.2.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp v1.2.0
	github.com/valyala/fasthttp v1.42.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp => ../fasthttp
