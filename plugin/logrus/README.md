# pplogrus
This package instruments the [sirupsen/logrus](https://github.com/sirupsen/logrus) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus)

This package allows additional transaction id and span id of the pinpoint span to be printed in the log message.
Use the NewField or NewEntry and pass the logrus field back to the logger.

``` go
tracer := pinpoint.FromContext(ctx)
logger.WithFields(pplogrus.NewField(tracer)).Fatal("oh, what a wonderful world")
```
``` go
entry := pplogrus.NewEntry(tracer).WithField("foo", "bar")
entry.Error("entry log message")
```

You can use NewHook as the logrus.Hook.
It is necessary to pass the context containing the pinpoint.Tracer to logrus.Logger.

``` go
logger.AddHook(pplogrus.NewHook())
entry := logger.WithContext(pinpoint.NewContext(context.Background(), tracer)).WithField("foo", "bar")
entry.Error("hook log message")
```

``` go
import (
    "github.com/sirupsen/logrus"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus"
)

func logging(w http.ResponseWriter, r *http.Request) {
    logger := logrus.New()
    tracer := pinpoint.TracerFromRequestContext(r)
    logger.WithFields(pplogrus.NewField(tracer)).Fatal("ohhh, what a world")
}
```
[Full Example Source](/plugin/logrus/example/logrus_example.go)
