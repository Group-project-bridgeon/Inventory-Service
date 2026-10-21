# HTTP logging and diagnostics

Inventory writes JSON to stdout using zerolog; it does not write local log files. Run `go run ./cmd/server` in a terminal to view application logs. The startup request configuration must include the Auth public key path, issuer and audience.

Each HTTP request gets a generated UUID request ID unless exactly one caller-supplied `X-Request-ID` matches `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`. The effective ID is returned in `X-Request-ID` and in errors as `request_id`. This value is correlation metadata only; it never identifies a user or trusted gateway.

HTTP completion events use `component=http`, `operation=complete`, method, matched route template (or `unmatched`), status, duration_ms and request_id. Successful requests below the configured slow threshold and routine 4xx responses are silent; slow requests are Warn. Unexpected failures produce one classified diagnostic from the handler, with an optional completion event that contains status/timing only. Probe successes are silent. Probe failures emit at most one warning per minute per process. Panic diagnostics contain function names from the stack, never the panic value, request data or stack arguments.

Known safe reasons include `context_canceled`, `timeout`, `query_canceled`, `postgres_server_error`, `probe_failed`, and `panic`; domain validation, duplicate codes, missing rows and default conflicts are ordinary HTTP outcomes and are not server-error diagnostics. Unknown failures use `unknown_failure` with a fixed generic summary. SQLSTATE is included only when it passes the five-character protocol format check.

Never log credentials, JWTs, Authorization headers, cookies, raw SQL arguments, bodies, connection strings, or PostgreSQL Message/Detail/Hint values. A safe sample unexpected database event:

```json
{"level":"error","service":"inventory","component":"postgres","operation":"create_warehouse","reason":"postgres_server_error","sqlstate":"08006","request_id":"a9eae68f-6ef1-41a6-a83b-b5e63ef2f1e3","method":"POST","route":"/api/v1/inventory/warehouses","message":"Database operation failed"}
```
