module github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql

go 1.15

require (
	github.com/denisenkom/go-mssqldb v0.12.2
	github.com/pinpoint-apm/pinpoint-go-agent v1.2.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.2.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
