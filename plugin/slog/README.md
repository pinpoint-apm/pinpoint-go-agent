# ppslog
This package instruments the standard library's [log/slog](https://pkg.go.dev/log/slog) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog)

This package allows additional transaction id and span id of the pinpoint span to be printed in the log message.
Wrap the handler of your logger with NewHandler. The ids are taken from the context passed to the log call,
so use the `*Context` methods of `slog.Logger`, which is what carries the pinpoint.Tracer.

``` go
logger := slog.New(ppslog.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
logger.ErrorContext(ctx, "handler log message")
```

The application's own attributes and groups are kept, and the two ids stay at the top level
even under `WithGroup`, since a qualified key is not the one the Pinpoint web UI looks for.

``` go
logger.WithGroup("req").With("foo", "bar").ErrorContext(ctx, "handler log message")
// {"msg":"handler log message","req":{"foo":"bar"},"PtxId":"...","PspanId":...}
```

Use NewAttrs where the tracer is at hand but the handler is not wrapped.

``` go
tracer := pinpoint.FromContext(ctx)
logger.LogAttrs(ctx, slog.LevelError, "oh, what a wonderful world", ppslog.NewAttrs(tracer)...)
```

``` go
import (
    "log/slog"
    "os"

    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog"
)

func logging(w http.ResponseWriter, r *http.Request) {
    logger := slog.New(ppslog.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
    logger.ErrorContext(r.Context(), "ohhh, what a world")
}
```
[Full Example Source](/plugin/slog/example/slog_example.go)
