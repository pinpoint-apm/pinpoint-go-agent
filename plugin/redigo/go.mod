module github.com/pinpoint-apm/pinpoint-go-agent/plugin/redigo

go 1.15

require (
	github.com/gomodule/redigo v1.8.9
	github.com/pinpoint-apm/pinpoint-go-agent v1.2.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.2.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
