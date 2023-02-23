module github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelastic

go 1.15

require (
	github.com/elastic/go-elasticsearch/v7 v7.10.0
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
