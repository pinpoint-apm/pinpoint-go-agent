module github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv7

go 1.15

require (
	github.com/go-redis/redis/v7 v7.4.1
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
