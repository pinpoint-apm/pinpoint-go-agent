module github.com/pinpoint-apm/pinpoint-go-agent/plugin/oracle

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.2.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.2.0
	github.com/sijms/go-ora/v2 v2.5.3
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
