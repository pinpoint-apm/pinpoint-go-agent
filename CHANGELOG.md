# Changelog

## Unreleased

- URL statistics recorded without a URI template are now keyed as `/NULL`
  (Java's `URITemplate.NULL_URI`, also used by the C++ agent) instead of
  `UNKNOWN_URL`. Server-side history under the old `UNKNOWN_URL` key does not
  carry over to the new key.
- SQL statements longer than 1 MiB are no longer normalized or recorded: no SQL
  annotation, no SQL metadata, and no `SQL.ErrorCount` increment. The 64KB
  metadata text cap is unchanged. The value matches the C++ agent; the
  drop policy is documented in `doc/java_parity.md`.
