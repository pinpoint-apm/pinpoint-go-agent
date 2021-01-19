module github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocql

go 1.12

require (
	github.com/gocql/gocql v0.0.0-20200815110948-5378c8f664e9
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
