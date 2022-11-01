module github.com/pinpoint-apm/pinpoint-go-agent/plugin/beegov2

go 1.15

require (
	github.com/beego/beego/v2 v2.0.5
	github.com/pinpoint-apm/pinpoint-go-agent v1.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
