module github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin

go 1.12

require (
	github.com/gin-gonic/gin v1.6.2
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
