# Runbook

## Start up

At start the service:

1. reads its configuration from the environment, and exits when `WORKLOAD_OIDC_ISSUER` is unset,
   unless `WORKLOAD_AUTH=disabled`, which it then warns about every 5 minutes;
2. starts OpenTelemetry export to `OTEL_EXPORTER_OTLP_ENDPOINT`;
3. connects to Redis at `REDIS_URL`;
4. sets up the client for the identity service at `IDENTITY_ADDR`, with its workload token from
   `WORKLOAD_TOKEN_FILE` when set (the connection is made lazily, on the first call);
5. serves gRPC on `GRPC_PORT`, authenticating every caller.

A failure in steps 1 to 3, a bad workload-auth setting, an unreadable `WORKLOAD_TOKEN_FILE`, a
malformed `REDIS_URL`, or a server error is logged at fatal level and the process exits non-zero. The identity service being down doesn't stop notify: group expansion
and actor labels degrade (groups add no one, labels fall back to user ids) until it is back.

## Refused callers

A refused call is logged at warn as `call refused`, with the method, the caller and the reason.
`Unauthenticated` means no token, a bad one, or a service account missing from
`WORKLOAD_ALLOWED_SERVICEACCOUNTS`; `PermissionDenied` means the caller is known but not allowed on
that method. `Unavailable` with `workload verifier unavailable` means no JWKS key set has loaded
yet: check that the issuer or `WORKLOAD_OIDC_JWKS_URL` is reachable. If identity refuses notify,
check that `WORKLOAD_TOKEN_FILE` is mounted and that identity lists `sneakers-notify`.

## Health

Use the standard gRPC health check. It needs no workload token, so a kubelet `grpc` probe works
as is, and so does a client that knows the health API without asking the server:

```bash
grpc_health_probe -addr localhost:9090
```

To see which build is running, ask for the response headers (`grpcurl -v`): the answer carries
`sneakers-version` and `sneakers-commit`. The image build stamps them from its `VERSION` and
`COMMIT` build arguments:

```bash
docker build --build-arg VERSION=v0.1.0 --build-arg COMMIT="$(git rev-parse HEAD)" .
```

Server reflection, which grpcurl uses to find a method, is not on any caller's allow-list, so
with authentication on it is refused. Give grpcurl the protos instead (`-import-path proto
-proto <file>`), or run locally with `WORKLOAD_AUTH=disabled`, where reflection works as before.

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
