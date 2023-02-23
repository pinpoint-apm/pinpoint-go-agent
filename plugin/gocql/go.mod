module github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocql

go 1.15

require (
	github.com/gocql/gocql v0.0.0-20200815110948-5378c8f664e9
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
