module github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
	go.mongodb.org/mongo-driver v1.5.1
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
