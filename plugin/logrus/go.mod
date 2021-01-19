module github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus

go 1.12

require (
	github.com/sirupsen/logrus v1.6.0
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.1.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
