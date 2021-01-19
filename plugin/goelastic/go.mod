module pinpoint/go-agent/plugin/goelastic

go 1.13

require (
	github.com/elastic/go-elasticsearch/v7 v7.10.0 // indirect
	github.com/elastic/go-elasticsearch/v8 v8.0.0-20200728144331-527225d8e836
	github.com/mailru/easyjson v0.7.3 // indirect
	github.com/olivere/elastic/v7 v7.0.19
	pinpoint/go-agent v0.1.0
	pinpoint/go-agent/plugin/http v0.1.0
)

replace pinpoint/go-agent => ../..

replace pinpoint/go-agent/plugin/http => ../../plugin/http
