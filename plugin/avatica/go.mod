module github.com/pinpoint-apm/pinpoint-go-agent/plugin/avatica

go 1.15

require (
	github.com/apache/calcite-avatica-go/v5 v5.2.0
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.1
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
)


replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http