module github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredis

go 1.12

require (
	github.com/go-redis/redis v6.10.0+incompatible
	go4.org v0.0.0-20201209231011-d4a079459e60 // indirect
	golang.org/x/sync v0.0.0-20190911185100-cd5d95a43a6e // indirect
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
