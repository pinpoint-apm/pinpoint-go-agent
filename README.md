![ci](https://github.com/pinpoint-apm/pinpoint-go-agent/workflows/ci/badge.svg)
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent)

# Pinpoint Go Agent

This is the official Go agent for [Pinpoint](https://github.com/pinpoint-apm/pinpoint).

Pinpoint Go Agent enables you to monitor Go applications using Pinpoint.
Go applications must be instrumented manually at the source code level,
because Go is a compiled language and does not have a virtual machine like Java.
Developers can instrument Go applications using the APIs provided in this package.

## Installation
```
go get github.com/pinpoint-apm/pinpoint-go-agent
```

## Requirements
* Go 1.21+
* Pinpoint 2.4.0+
* Linux, OS X, and Windows are supported.

## Getting Started

Refer [Quick Start](doc/quick_start.md) for simple test run of Pinpoint Go Agent.

## Plug-ins
Pinpoint Go Agent provides support for instrumenting Go’s built-in http package, database/sql drivers
and plug-ins for popular frameworks and toolkits.
These packages help you to make instruments with simple source code modifications.
Refer the [Plugin User Guide](doc/plugin_guide.md) for more information.

## Documents
* [Quick Start](doc/quick_start.md)
* [Configuration](doc/config.md)
* [Plugin User Guide](doc/plugin_guide.md)
* [Custom Instrumentation](doc/instrument.md)
* [Troubleshooting](doc/troubleshooting.md)

## Contributing

We are looking forward to your contributions via pull requests.
For tips on contributing code fixes or enhancements, please see the [contributing guide](CONTRIBUTING.md).
To report bugs, please create an Issue on the GitHub repository. 

## License

Pinpoint is licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for full license text.
