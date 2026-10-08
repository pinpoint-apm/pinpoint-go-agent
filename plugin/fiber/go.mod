module github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber/v2

go 1.25.0

require (
	github.com/gofiber/fiber/v2 v2.52.14
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2 v2.0.0
	github.com/pinpoint-apm/pinpoint-go-agent/v2 v2.0.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/shoenig/go-m1cpu v0.1.6 // indirect
	github.com/valyala/fasthttp v1.73.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
)

require (
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.19.1 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2 v2.0.0
	github.com/pkg/errors v0.9.1 // indirect
	github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c // indirect
	github.com/rivo/uniseg v0.2.0 // indirect
	github.com/shirou/gopsutil/v3 v3.24.5 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/spaolacci/murmur3 v1.1.0 // indirect
	github.com/tklauser/go-sysconf v0.3.12 // indirect
	github.com/tklauser/numcpus v0.6.1 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/grpc v1.83.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/pinpoint-apm/pinpoint-go-agent/v2 => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2 => ../http

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2 => ../fasthttp
