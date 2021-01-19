module github.com/pinpoint-apm/pinpoint-go-agent/example

go 1.12

require (
	github.com/go-sql-driver/mysql v1.5.0
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../plugin/http

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql => ../plugin/mysql
