module github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.2.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.2.0
	go.mongodb.org/mongo-driver v1.3.5
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
