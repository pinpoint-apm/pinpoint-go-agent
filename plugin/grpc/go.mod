module github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc

go 1.15

require (
	github.com/golang/protobuf v1.5.2
	github.com/pinpoint-apm/pinpoint-go-agent v1.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.1.0
	google.golang.org/grpc v1.49.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
