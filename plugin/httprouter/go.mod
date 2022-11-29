module github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter

go 1.15

require (
	github.com/julienschmidt/httprouter v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent v1.2.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.2.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
