module github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
	github.com/sirupsen/logrus v1.8.1
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
