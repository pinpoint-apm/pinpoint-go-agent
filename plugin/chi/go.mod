module github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi

go 1.15

require (
	github.com/go-chi/chi/v5 v5.0.7
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http
