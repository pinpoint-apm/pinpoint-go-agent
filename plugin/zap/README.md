# ppzap
This package instruments the [uber-go/zap](https://github.com/uber-go/zap) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/zap
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/zap"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/zap)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/zap)

This package allows additional transaction id and span id of the pinpoint span to be printed in the log message.
Use the NewField and pass the zap fields back to the logger.

``` go
tracer := pinpoint.FromContext(ctx)
logger.Error("oh, what a wonderful world", ppzap.NewField(tracer)...)
```

Or derive a logger once per request with NewLogger. It keeps the fields and options
the provided logger already carries.

``` go
logger := ppzap.NewLogger(zap.L(), tracer).With(zap.String("foo", "bar"))
logger.Error("logger log message")
```

Unlike the [slog](/plugin/slog) and [logrus](/plugin/logrus) plugins, this one has no
handler or hook that reads the tracer on its own: zap passes no `context.Context` to
`zapcore.Core`, so the span has to be named where the logger is derived.

For a `*zap.SugaredLogger`, derive it from an instrumented `*zap.Logger`:

``` go
sugar := ppzap.NewLogger(logger, tracer).Sugar()
sugar.Errorw("sugared log message", "foo", "bar")
```

``` go
import (
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/zap"
    "go.uber.org/zap"
)

func logging(w http.ResponseWriter, r *http.Request) {
    logger, _ := zap.NewProduction()
    defer logger.Sync()

    tracer := pinpoint.FromContext(r.Context())
    logger.Error("ohhh, what a world", ppzap.NewField(tracer)...)
}
```
[Full Example Source](/plugin/zap/example/zap_example.go)
