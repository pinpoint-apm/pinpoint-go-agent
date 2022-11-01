module github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9

go 1.15

require (
	github.com/go-redis/redis/v9 v9.0.0-rc.1
	github.com/pinpoint-apm/pinpoint-go-agent v1.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
