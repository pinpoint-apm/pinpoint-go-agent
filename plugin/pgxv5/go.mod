module github.com/pinpoint-apm/pinpoint-go-agent/plugin/pppgxv5

go 1.15

require (
	github.com/jackc/pgx/v5 v5.0.0
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
