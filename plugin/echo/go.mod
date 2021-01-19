module github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo

go 1.12

require (
	github.com/labstack/echo v3.3.10+incompatible
	github.com/labstack/echo/v4 v4.0.0
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
