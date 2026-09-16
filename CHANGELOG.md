# Changelog

## Unreleased

### Breaking

- **The HTTP client URL annotation strips the query by default.** `pphttp`
  and `ppfasthttp` record `HTTP.URL` up to the `?` unless the new
  `Http.Client.RecordUrlQuery` (default `false`) is on, matching the C++ agent;
  Java's `profiler.<plugin>.param` defaults on. Query strings carry tokens and
  ids. Endpoint and destination are unchanged.

### Added

- **Configurable real-IP headers.** `Http.Server.RealIpHeader` (ordered list,
  default `["X-Forwarded-For", "X-Real-Ip"]` = today's behaviour, `[]` trusts
  none) and `Http.Server.RealIpEmptyValue` port Java's `RealIpHeaderResolver`:
  a `Forwarded` header is parsed for its `for=` token, other headers give
  their first hop, a value equal to the empty value is skipped. Both
  reloadable.
- **Request parameter recording, opt-in.** `pphttp.RecordHttpServerRequestWithQuery`
  (used by `RecordHttpServerRequest`, so every net/http based plugin gets it)
  records the query string as annotation 41 (`HTTP.PARAM`) in Java's
  `HttpServletParameterExtractor` format when `Http.Server.RecordRequestParam`
  is on (default `false`; Java defaults on). `pphttp.FormatRequestParams` is
  exported for adapters that hold the query outside a net/http request.

- `Http.UrlStat.LimitSize` now defaults to `1000`, Java's
  `profiler.uri.stat.completed.data.limit.size`, instead of `1024`; the C++
  agent made the same move, so the two ports and Java drop a tick's excess
  URIs at the same point. `Http.UrlStat.QueueSize` stays `1024`: it bounds a
  different queue - the input queue between the request path and the
  aggregator, which Java sizes at 5192 on one queue and neither port
  reproduces.
- `Stat.CollectInterval` is capped at `10000` ms, Java's
  `DefaultAgentStatMonitor` maximum, instead of `60000`; a larger value falls
  back to the default as before. A six-minute stat batch is no longer
  reachable by misconfiguration.
- The metadata retry budget and rejection policy are locked
  (`Test_MetadataRetryBudget`), mirrored in the C++ suite:
  both ports drop a `PResult.success=false` reply where Java retries it, so
  a change in either port is now a deliberate joint change.

- A span that drops exception entries at the `Error.MaxChainDepth` entry limit
  now logs how many it dropped when it ends. The existing warning latches after
  the first drop, so it said a span hit the limit but not by how much, and a
  retry loop that lost a handful of chain links read exactly like one that lost
  thousands. The limit itself is unchanged (10 to 64 entries a span, derived
  from `Error.MaxChainDepth`'s own clamp ceiling). It is not raised to the C++
  agent's 100 because the cap here is derived from the option rather than set
  independently, so one chain of the configured depth is always kept whole;
  Java's mid-span buffer flush is not ported because sending exception metadata
  before the span ends is a separate design, not a constant.
- **Default behavior change.** The collector channel now uses the gRPC `dns`
  resolver (`dns:///host:port`) instead of the `passthrough` scheme. A collector
  host with several A records is resolved into the channel's full address list,
  so the agent gains client-side spreading across collector instances and
  failover to another address, and the list is re-resolved as records change;
  `passthrough` gave the channel a single address, which left the
  `Collector.Grpc.ConnectionMaxAge` load balancing policy (ported from Java's
  `SubconnectionExpiringLoadBalancer`) nothing to spread over and its
  re-resolution request nothing to re-resolve. IP literals (IPv4 and IPv6) and
  names in `/etc/hosts`, including the default `localhost`, are unchanged, as is
  TLS verification, which keeps deriving the server name from the channel
  authority - the collector hostname. The new `Collector.Grpc.DnsResolverEnable`
  (default true) restores the `passthrough` scheme when set to false, as a
  rollback lever that needs no redeploy.
- New `pinpoint.ShutdownOnSignal(agent, sigs...) (stop func())` calls
  `Shutdown()` when the process receives one of the given signals (`SIGTERM`
  and `SIGINT` by default), then restores the default signal handling and
  re-raises the signal so the process still exits with `128+signum`. It is
  opt-in and off by default: the agent never calls `signal.Notify` on its
  own. Without it, a `defer agent.Shutdown()` does not run on `SIGTERM` - the
  signal every container orchestrator sends on a rollout - and the spans still
  queued are lost while the UI keeps listing the agent as alive. `os.Exit`
  cannot be covered by any means; see `doc/troubleshooting.md`. The C++ agent
  takes the same opt-in policy with a different mechanism (`std::atexit`, no
  signal handler); see `doc/development.md`.
- The active span registry behind the active-request histogram is now bounded
  at 10240 entries (320 per shard), the Java agent's `DefaultActiveTraceRepository`
  maximum. A span that is never ended used to leave its entry behind forever;
  now a full shard evicts an existing entry for the new span and logs a
  rate-limited warning naming the size and the cap. Only an application that
  leaks spans reaches the cap; the histogram it then reports covers the most
  recent 10240 spans rather than all of them.
- URL statistics recorded without a URI template are now keyed as `/NULL`
  (Java's `URITemplate.NULL_URI`, also used by the C++ agent) instead of
  `UNKNOWN_URL`. Server-side history under the old `UNKNOWN_URL` key does not
  carry over to the new key.
- SQL statements longer than 1 MiB are no longer normalized or recorded: no SQL
  annotation, no SQL metadata, and no `SQL.ErrorCount` increment. The 64KB
  metadata text cap is unchanged. The value matches the C++ agent; the
  drop policy is documented in `doc/development.md`.
