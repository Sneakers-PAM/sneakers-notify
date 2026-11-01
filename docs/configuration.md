# Configuration

The service reads its configuration from the environment. Every setting has a default, except
service-to-service authentication (below), which must be configured or explicitly disabled.

| Variable | Default | Purpose |
|---|---|---|
| `GRPC_PORT` | `9090` | TCP port the gRPC server listens on (all interfaces). |
| `REDIS_URL` | `redis://localhost:26379/0` | Redis holding the inboxes, as a `redis://[:password@]host:port/db` or `rediss://[:password@]host:port/db` URL. The address, database number, password and `rediss://` TLS are applied, and the certificate is checked against the system roots. A username or a query option (such as `?pool_size=5`) stops the start rather than being ignored. Keep the password in your secret store and inject the URL at run time. |
| `IDENTITY_ADDR` | `localhost:9192` | `host:port` of the identity service's gRPC API, used to expand groups and label actors. The connection is plaintext. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (none) | OTLP gRPC collector for traces and metrics (and logs when `OTLP_LOGS_ENABLED` is on), as a URL: `http://collector:4317` for plaintext, `https://collector:4317` for TLS. A bare `host:port` still connects as plaintext, but the OpenTelemetry SDK, which reads this variable too, logs `parse url` errors at start. Any other scheme stops the service at start. Unset or empty runs without a collector: no export, no error log. |
| `OTLP_INSECURE_SKIP_VERIFY` | (off) | `true` skips checking the collector's certificate on an `https` endpoint. The connection stays encrypted but the collector is no longer authenticated, and logs are not exported while it is on. Any other value leaves checking on. |
| `OTLP_LOGS_ENABLED` | (off) | `true` also exports logs to the collector, filtered as described under [Log export](#log-export). Any other value exports none. Stdout is unaffected. |
| `LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic` or `disabled`. |
| `LOG_FORMAT` | `json` | `json`, `console` (or `pretty`), or `both` (JSON on stdout, console on stderr). |

Local development logs at `trace` in `console` format (see `.env.example`). Cluster environments
log JSON, at `debug` in a dev cluster, `info` in QA or staging and `error` in production.

## Log export

Exported logs are default-deny, and the filters apply to the export only. A record carries its time, level, `service`, message and trace ids, plus the fields named in `internal/telemetry`. Every other field, `error` included, is dropped: a new log field exports nothing until it is added to that list. Only ids, counts, durations, classes, states and short enums belong on it. Levels below `info` are never exported, and strings go through go-otel's redactor plus a rule for `snk_` API keys. `LOG_FORMAT=console` exports nothing, because only JSON lines are read.

## Trace and metric export

Traces, metrics and the resource are default-deny too, through go-otel's telemetry filter. Span names leave only when they match `^sneakers\.[a-z]+\.v1\.[A-Za-z]+/[A-Za-z]+$`; any other span stays in the trace as `[span]`. The only attributes on spans and metrics are `rpc.method`, held to the same pattern, and `rpc.response.status_code`. The resource carries `service.name` alone. Status text, exception events, `tracestate` and baggage never leave, and baggage is not carried on calls.

## Export health

When `OTEL_EXPORTER_OTLP_ENDPOINT` is set, the readiness report lists an optional `otlp` dependency. It reports degraded, with the class `unavailable`, when the last export of traces or metrics failed, or of logs when they are on. This marks the service degraded and never fails readiness, and it is left out when no endpoint is set. OpenTelemetry's own errors print as a gRPC code only, through go-otel's redacted handler, with no error text.

## Service-to-service authentication

Notify checks every caller's workload identity and presents its own when it calls identity. The
code is the owner's helper package
[`github.com/Bugs5382/go-workload-identity`](https://github.com/Bugs5382/go-workload-identity)
(v1.0.0), which every Sneakers service imports in place of its old private copy.
`internal/server/workloadauth.go` sets the Sneakers values the package has no default for: the
audience `sneakers` when `WORKLOAD_AUDIENCE` is unset, and the caller-name prefix `sneakers-`
(`WORKLOAD_SERVICEACCOUNT_PREFIX` is not read).

As a callee:

| Variable | Default | Purpose |
|---|---|---|
| `WORKLOAD_OIDC_ISSUER` | (required) | The cluster's ServiceAccount token issuer (`https://`). The token's `iss` must equal it. |
| `WORKLOAD_OIDC_JWKS_URL` | discovered | JWKS URL (`https://`); when unset it's read from the issuer's OpenID configuration. |
| `WORKLOAD_OIDC_CA_FILE` | system roots | Extra PEM CA bundle for discovery and the JWKS fetch. |
| `WORKLOAD_OIDC_BEARER_FILE` | (unset) | Bearer token sent on discovery and the JWKS fetch, re-read on every fetch. |
| `WORKLOAD_AUDIENCE` | `sneakers` | The token's `aud` must contain it. |
| `WORKLOAD_ALLOWED_SERVICEACCOUNTS` | (required) | Comma list of `<namespace>/<serviceaccount>`: for notify, `<ns>/sneakers-vault,<ns>/sneakers-gateway`. |
| `WORKLOAD_AUTH` | (unset) | `disabled` turns the check off, for local development only: every caller that reaches the port is trusted, and a warning is logged at start and every 5 minutes. No other value is accepted. |

Without `WORKLOAD_OIDC_ISSUER` the service refuses to start, unless `WORKLOAD_AUTH=disabled`;
setting both is refused too.

As a caller (to identity):

| Variable | Default | Purpose |
|---|---|---|
| `WORKLOAD_TOKEN_FILE` | (unset) | Path of the projected ServiceAccount token (audience `sneakers`), normally `/var/run/secrets/sneakers/token`. Sent on every identity call and re-read each time, so a rotated token is picked up. A set path that can't be read stops the start. Unset sends no token, which only an identity with authentication off accepts. |

No caller can be checked before the issuer's key set has loaded, so readiness waits for it too:
`/readyz` and the gRPC health check answer `NOT_SERVING`, with `workload-identity` reported down
in the readiness body (`server.WorkloadIdentity`, checking `Verifier.Ready`), until then. It's
left out of the readiness body when `WORKLOAD_AUTH=disabled`. Liveness is unaffected.

Example (cluster):

```bash
WORKLOAD_OIDC_ISSUER=https://kubernetes.default.svc.cluster.local
WORKLOAD_OIDC_CA_FILE=/var/run/secrets/tokens/ca.crt
WORKLOAD_OIDC_BEARER_FILE=/var/run/secrets/tokens/token
WORKLOAD_AUDIENCE=sneakers
WORKLOAD_ALLOWED_SERVICEACCOUNTS=sneakers/sneakers-vault,sneakers/sneakers-gateway
WORKLOAD_TOKEN_FILE=/var/run/secrets/sneakers/token
```

## Fixed limits

These are constants in the code, not settings:

- each user's inbox keeps the newest 100 items;
- an inbox expires 90 days after its last new item;
- `ListNotifications` returns at most 100 items, whatever `limit` asks for.
