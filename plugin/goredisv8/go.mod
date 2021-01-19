module github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8

go 1.12

require (
	github.com/go-redis/redis/v8 v8.0.0-beta.7
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
