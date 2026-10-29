# ppslog
This package instruments the standard library's [log/slog](https://pkg.go.dev/log/slog) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog/v2)

This package allows additional transaction id and span id of the pinpoint span to be printed in the log message.
Wrap the handler of your logger with NewHandler. The ids are taken from the context passed to the log call,
so use the `*Context` methods of `slog.Logger`, which is what carries the pinpoint.Tracer.

``` go
logger := slog.New(ppslog.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
logger.ErrorContext(ctx, "handler log message")
```

The application's own attributes and groups are kept. Under `WithGroup`, the two ids
are qualified by that group too. The underlying handler processes `With` attributes
once; logging never replays them or reevaluates their values.

This changes the previous top-level placement of ids under groups. Log consumers
must use the grouped keys. For top-level ids required by the Pinpoint web UI, use
`NewAttrs` on an unwrapped logger before adding groups, once per request.

``` go
logger.WithGroup("req").With("foo", "bar").ErrorContext(ctx, "handler log message")
// {"msg":"handler log message","req":{"foo":"bar","PtxId":"...","PspanId":...}}
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

    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog/v2"
)

func logging(w http.ResponseWriter, r *http.Request) {
    logger := slog.New(ppslog.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
    logger.ErrorContext(r.Context(), "ohhh, what a world")
}
```
[Full Example Source](/example/slog/slog_example.go)
