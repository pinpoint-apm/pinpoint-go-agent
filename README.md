![ci](https://github.com/pinpoint-apm/pinpoint-go-agent/workflows/ci/badge.svg)
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/v2)

# Pinpoint Go Agent

This is the official Go agent for [Pinpoint](https://github.com/pinpoint-apm/pinpoint).

Pinpoint Go Agent enables you to monitor Go applications using Pinpoint.
Go applications must be instrumented manually at the source code level,
because Go is a compiled language and does not have a virtual machine like Java.
Developers can instrument Go applications using the APIs provided in this package.

## Installation
```
go get github.com/pinpoint-apm/pinpoint-go-agent/v2
```

## Requirements
* Go 1.25+
* Pinpoint 3.1.0+
* Linux, OS X, and Windows are supported.

Moving from v1? See [Migrating from v1 to v2](doc/migration_v2.md).

## Getting Started

Refer [Getting Started](doc/getting_started.md) for simple test run of Pinpoint Go Agent.

## Plug-ins
Pinpoint Go Agent provides support for instrumenting Go’s built-in http package, database/sql drivers
and plug-ins for popular frameworks and toolkits.
These packages help you to make instruments with simple source code modifications.
Refer the [Plugin User Guide](doc/plugin_guide.md) for more information.

## Documents
* [Getting Started](doc/getting_started.md) - install, configure and verify your first trace
* [Migrating from v1 to v2](doc/migration_v2.md) - the import paths, APIs and defaults that changed
* [Configuration](doc/config.md) - every option, plus development, production and container examples
* [Plugin User Guide](doc/plugin_guide.md) - the supported frameworks, drivers and clients
* [Custom Instrumentation](doc/instrument.md) - trace what no plugin covers
* [Tracer, Span, and Annotation Contracts](doc/api_contracts.md) - the API rules to keep
* [Troubleshooting](doc/troubleshooting.md) - when the trace does not show up
* [Development Guide](doc/development.md) - build, test and extend the agent itself

## Contributing

We are looking forward to your contributions via pull requests.
For tips on contributing code fixes or enhancements, please see the [contributing guide](CONTRIBUTING.md).
To report bugs, please create an Issue on the GitHub repository. 

## License

Pinpoint is licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for full license text.
