module github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber

go 1.15

require (
	github.com/gofiber/fiber/v2 v2.37.0
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
	github.com/valyala/fasthttp v1.39.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
