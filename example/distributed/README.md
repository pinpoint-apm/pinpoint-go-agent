# Distributed tracing demo: proxy → server → MySQL

Two HTTP apps traced by the Pinpoint Go agent:

- **[proxy](proxy/proxy.go)** (`GoProxyExample`, port 8080) — receives
  `GET /api/members`, injects the trace context, and forwards the call to the
  backend server.
- **[server](server/server.go)** (`GoDbServerExample`, port 8081) — continues
  the received trace and answers the call from MySQL through the
  [mysql plugin](/plugin/mysql), then hands follow-up work to a goroutine
  traced with an async span.

In the Pinpoint server map one request shows up as a single distributed trace:

```
client → GoProxyExample → GoDbServerExample → MySQL
```

Both apps record the `User-Agent` request header
(`WithHttpServerRecordRequestHeader`; see [doc/config.md](/doc/config.md)).

## Run

[`run.sh`](run.sh) starts a MySQL container (unless port 3306 is already
taken) plus both apps, and stops them together on Ctrl-C. A reachable
Pinpoint collector is required (for a full stack see
[pinpoint-docker](https://github.com/pinpoint-apm/pinpoint-docker)):

```bash
PINPOINT_GO_COLLECTOR_HOST=my-collector example/distributed/run.sh
```

The server creates and seeds its `members` table at startup; if the database
is unreachable it still boots and answers **503**, so the two-app trace can be
inspected without one. The container `run.sh` starts is the same as:

```bash
docker run --rm -p 3306:3306 -e MYSQL_ROOT_PASSWORD=p123 -e MYSQL_DATABASE=testdb mysql:8.0
```

Generate traffic:

```bash
curl http://localhost:8080/api/members
```

Then open the Pinpoint web UI and select the `GoProxyExample` application.

## Wiring

| Variable | Default | Used by |
|---|---|---|
| `BACKEND` | `http://localhost:8081` | proxy — backend base URL |
| `MYSQL_DSN` | `root:p123@tcp(127.0.0.1:3306)/testdb` | server — database connection |
| `ADDR` | `:8080` / `:8081` | both — listen address |
| `PINPOINT_GO_COLLECTOR_HOST` | `localhost` | both — [agent configuration](/doc/config.md) |
