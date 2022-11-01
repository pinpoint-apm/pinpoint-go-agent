module github.com/pinpoint-apm/pinpoint-go-agent/example

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.1.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql v1.1.0
	gorm.io/driver/mysql v1.4.3
	gorm.io/gorm v1.24.0
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../plugin/http

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql => ../plugin/mysql
