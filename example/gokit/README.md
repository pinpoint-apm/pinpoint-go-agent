# go-kit tracing: no plugin needed

[go-kit/kit](https://github.com/go-kit/kit) transports are thin adapters over
`net/http` and grpc-go, so the existing [http](/plugin/http) and
[grpc](/plugin/grpc) plugins trace them as-is and the tracer reaches the
go-kit endpoint through `ctx`:

| go-kit piece | Why it is already traced | Wire it with |
|---|---|---|
| `httptransport.Server` | it is an `http.Handler`; `ServeHTTP` passes `r.Context()` to the endpoint | `pphttp.WrapHandler(httptransport.NewServer(...))` |
| `httptransport.Client` | sends `req.WithContext(ctx)` through any client with a `Do` method | `httptransport.SetClient(pphttp.WrapClient(nil))` |
| `grpctransport.Server` | a handler behind a generated grpc-go service | `grpc.NewServer(grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()), ...)` |
| `grpctransport.Client` | calls `conn.Invoke(ctx, ...)` | `grpc.NewClient(..., grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()))` |

Inside an endpoint, `pinpoint.FromContext(ctx)` returns the tracer.

## The demo

- **[server](server/server.go)** (`GoKitGrpcServer`, port 8080) — go-kit gRPC
  service, traced by the ppgrpc server interceptors.
- **[gateway](gateway/gateway.go)** (`GoKitHttpGateway`, port 8000) — go-kit
  HTTP service, traced by `pphttp.WrapHandler`.
  `GET /hello?name=x` calls the server through a go-kit gRPC client;
  `GET /proxy?name=x` calls its own `/hello` through a go-kit HTTP client.

```
curl 'http://localhost:8000/proxy?name=kit'
  → GoKitHttpGateway (/proxy) → GoKitHttpGateway (/hello) → GoKitGrpcServer
```

```bash
go run ./gokit/server & go run ./gokit/gateway
```

Both read `$HOME/tmp/pinpoint-config.yaml`; see [doc/config.md](/doc/config.md).
