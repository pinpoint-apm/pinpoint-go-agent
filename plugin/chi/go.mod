module github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi

go 1.12

require (
	github.com/go-chi/chi v4.1.2+incompatible
	github.com/pinpoint-apm/pinpoint-go-agent v0.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v0.0.0-00010101000000-000000000000
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../../plugin/http
