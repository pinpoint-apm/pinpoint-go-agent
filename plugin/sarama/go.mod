module github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama

go 1.15

require (
	github.com/Shopify/sarama v1.26.4
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
