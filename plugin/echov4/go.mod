module github.com/pinpoint-apm/pinpoint-go-agent/plugin/echov4

go 1.15

require (
	github.com/labstack/echo/v4 v4.9.0
	github.com/pinpoint-apm/pinpoint-go-agent v1.4.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.4.0
	github.com/stretchr/testify v1.8.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
