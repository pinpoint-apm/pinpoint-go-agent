module github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.4.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.4.0
	github.com/redis/go-redis/v9 v9.0.2
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
