module github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm

go 1.15

require (
	github.com/pinpoint-apm/pinpoint-go-agent v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/http v1.3.0
	github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql v1.3.0
	gorm.io/driver/mysql v1.3.3
	gorm.io/gorm v1.23.4
)

replace github.com/pinpoint-apm/pinpoint-go-agent => ../..

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/http => ../http

replace github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql => ../mysql
