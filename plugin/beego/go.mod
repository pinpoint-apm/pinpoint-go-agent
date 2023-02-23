module github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego

go 1.15

require (
	github.com/beego/beego/v2 v2.0.5
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
