module github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8

go 1.15

require (
	github.com/go-redis/redis/v8 v8.11.5
	github.com/pinpoint-apm/pinpoint-go-agent v1.2.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.2.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
