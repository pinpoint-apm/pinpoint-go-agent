module github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter/v2

go 1.25.0

require (
	github.com/fasthttp/router v1.5.4
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2 v2.0.0
	github.com/pinpoint-apm/pinpoint-go-agent/v2 v2.0.0
	github.com/stretchr/testify v1.8.0
	github.com/valyala/fasthttp v1.73.0
)

require (
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.19.1 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2 v2.0.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c // indirect
	github.com/savsgio/gotils v0.0.0-20240704082632-aef3928b8a38 // indirect
	github.com/shirou/gopsutil/v3 v3.22.7 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/spaolacci/murmur3 v1.1.0 // indirect
	github.com/tklauser/go-sysconf v0.3.10 // indirect
	github.com/tklauser/numcpus v0.4.0 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.2 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.0.0-20201208040808-7e3f01d25324 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/grpc v1.82.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/pinpoint-apm/pinpoint-go-agent/v2 => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2 => ../http

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2 => ../fasthttp
