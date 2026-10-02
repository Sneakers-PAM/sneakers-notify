# Runbook

## Start up

At start the service:

1. reads its configuration from the environment;
2. starts OpenTelemetry export to `OTEL_EXPORTER_OTLP_ENDPOINT`;
3. connects to Redis at `REDIS_URL`;
4. sets up the client for the identity service at `IDENTITY_ADDR` (the connection is made lazily,
   on the first call);
5. serves gRPC on `GRPC_PORT`.

A failure in steps 2 or 3, a malformed `REDIS_URL`, or a server error is logged at fatal level and
the process exits non-zero. The identity service being down doesn't stop notify: group expansion
and actor labels degrade (groups add no one, labels fall back to user ids) until it is back.

## Health

Use the standard gRPC health check:

```bash
grpcurl -plaintext localhost:9090 grpc.health.v1.Health/Check
```

## Data

Each user's inbox is one Redis list, `sneakers:notif:<user id>`, holding JSON items, newest first,
trimmed to 100 and set to expire 90 days after the last item was added. There is nothing else to
back up: losing Redis empties the inboxes and the badges, and the audit trail still has every
event. Point notify at its own Redis database (or a database number nobody else uses) so it never
trims or expires another service's keys.

## Shutdown

On SIGINT or SIGTERM the server stops accepting new calls and waits up to 10 seconds for in-flight
calls to finish before stopping.

## Panics

A panic in a handler is recovered: the caller gets a generic `Internal` error, and the panic value
and stack go only to the log (at error level) and to the active trace span.
