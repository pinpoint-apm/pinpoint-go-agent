module github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredis

go 1.15

require (
	github.com/go-redis/redis v6.10.0+incompatible
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
