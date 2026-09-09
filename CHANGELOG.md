# Changelog

## Unreleased

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
  drop policy is documented in `doc/java_parity.md`.
