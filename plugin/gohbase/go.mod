module github.com/pinpoint-apm/pinpoint-go-agent/plugin/gohbase

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.1.0
	github.com/tsuna/gohbase v0.0.0-20200820233321-d669aff6255b
	modernc.org/strutil v1.1.3 // indirect
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
