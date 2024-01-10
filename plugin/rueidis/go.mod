module github.com/pinpoint-apm/pinpoint-go-agent/plugin/rueidis

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.4.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.4.0
	github.com/redis/rueidis v1.0.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
