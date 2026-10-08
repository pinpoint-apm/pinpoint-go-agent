# ppmongomonitor
Package ppmongomonitor holds what the [mongodriver](../mongodriver) and
[mongodriverv2](../mongodriverv2) plugins share: the `Mongo.*` options, the
span map that pairs a command's started event with its finished one, and the
host and JSON helpers. An application installs one of those plugins'
`NewMonitor`; it does not import this package directly.

[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongomonitor/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongomonitor/v2)
