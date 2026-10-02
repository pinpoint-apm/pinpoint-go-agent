module github.com/pinpoint-apm/pinpoint-go-agent/v2/test/e2e

go 1.25.0

require (
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2 v2.0.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2 v2.0.0
	github.com/pinpoint-apm/pinpoint-go-agent/v2 v2.0.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c // indirect
	github.com/shirou/gopsutil/v3 v3.22.7 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/spaolacci/murmur3 v1.1.0 // indirect
	github.com/tklauser/go-sysconf v0.3.10 // indirect
	github.com/tklauser/numcpus v0.4.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.2 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.0.0-20201208040808-7e3f01d25324 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/pinpoint-apm/pinpoint-go-agent/v2 => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2 => ../../plugin/http

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2 => ../../plugin/grpc
