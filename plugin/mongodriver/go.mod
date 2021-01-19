module github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver

go 1.12

require (
	go.mongodb.org/mongo-driver v1.3.5
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
