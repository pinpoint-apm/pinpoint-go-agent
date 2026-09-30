# ppmongo
This package instruments the [mongodb/mongo-go-driver](https://github.com/mongodb/mongo-go-driver) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver/v2)

This package instruments the mongo-go-driver calls.
Use the NewMonitor as Monitor field of mongo-go-driver's ClientOptions.

``` go
opts := options.Client()
opts.Monitor = ppmongo.NewMonitor()
client, err := mongo.Connect(ctx, opts)
```

It is necessary to pass the context containing the pinpoint.Tracer to mongo.Client.

``` go
collection := client.Database("testdb").Collection("example")
ctx := pinpoint.NewContext(context.Background(), tracer)
collection.InsertOne(ctx, bson.M{"foo": "bar", "apm": "pinpoint"})
```

``` go
import (
    "go.mongodb.org/mongo-driver/bson"
    "go.mongodb.org/mongo-driver/mongo"
    "go.mongodb.org/mongo-driver/mongo/options"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver/v2"
)

func mongodb(w http.ResponseWriter, r *http.Request) {
    opts := options.Client()
    opts.ApplyURI("mongodb://localhost:27017")
    opts.Monitor = ppmongo.NewMonitor()
    client, err := mongo.Connect(context.Background(), opts)

    collection := client.Database("testdb").Collection("example")
    _, err = collection.InsertOne(r.Context(), bson.M{"foo": "bar", "apm": "pinpoint"})
    ...
}
```
[Full Example Source](/example/mongodriver/mongo_example.go)
