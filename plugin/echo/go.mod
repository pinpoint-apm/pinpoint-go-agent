module github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo

go 1.15

require (
	github.com/labstack/echo v3.3.10+incompatible
	github.com/labstack/gommon v0.4.0 // indirect
	github.com/pinpoint-apm/pinpoint-go-agent v1.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.1.0
	github.com/stretchr/testify v1.8.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
