# ppmongov2
This package instruments the [mongodb/mongo-go-driver/v2](https://github.com/mongodb/mongo-go-driver) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriverv2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriverv2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriverv2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriverv2)

This package instruments the mongo-go-driver v2 calls.
Use the NewMonitor as Monitor field of mongo-go-driver's ClientOptions.

``` go
opts := options.Client()
opts.Monitor = ppmongov2.NewMonitor()
client, err := mongo.Connect(opts)
```

It is necessary to pass the context containing the pinpoint.Tracer to mongo.Client.

``` go
collection := client.Database("testdb").Collection("example")
ctx := pinpoint.NewContext(context.Background(), tracer)
collection.InsertOne(ctx, bson.M{"foo": "bar", "apm": "pinpoint"})
```

``` go
import (
    "go.mongodb.org/mongo-driver/v2/bson"
    "go.mongodb.org/mongo-driver/v2/mongo"
    "go.mongodb.org/mongo-driver/v2/mongo/options"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriverv2"
)

func mongodb(w http.ResponseWriter, r *http.Request) {
    opts := options.Client()
    opts.ApplyURI("mongodb://localhost:27017")
    opts.Monitor = ppmongov2.NewMonitor()
    client, err := mongo.Connect(opts)

    collection := client.Database("testdb").Collection("example")
    _, err = collection.InsertOne(r.Context(), bson.M{"foo": "bar", "apm": "pinpoint"})
    ...
}
```
[Full Example Source](/plugin/mongodriverv2/example/mongov2_example.go)
