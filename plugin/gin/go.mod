module github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin

go 1.15

require (
	github.com/gin-gonic/gin v1.7.7
	github.com/pinpoint-apm/pinpoint-go-agent v1.2.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.2.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
