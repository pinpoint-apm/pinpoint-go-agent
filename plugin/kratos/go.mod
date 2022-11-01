module github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos

go 1.15

require (
	github.com/go-kratos/kratos/v2 v2.5.2
	github.com/pinpoint-apm/pinpoint-go-agent v1.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.1.0
	google.golang.org/genproto v0.0.0-20220519153652-3a47de7e79bd
	google.golang.org/grpc v1.49.0
	google.golang.org/protobuf v1.28.1
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
