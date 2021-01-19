module github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc

go 1.12

require (
	github.com/golang/protobuf v1.4.2
	google.golang.org/grpc v1.30.0
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
