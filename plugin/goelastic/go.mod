module github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelastic

go 1.13

require (
	github.com/elastic/go-elasticsearch/v7 v7.10.0
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
