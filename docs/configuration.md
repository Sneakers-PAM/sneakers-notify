# Configuration

The service reads its configuration from the environment. Every setting has a default, except
service-to-service authentication (below), which must be configured or explicitly disabled.

| Variable | Default | Purpose |
|---|---|---|
| `GRPC_PORT` | `9090` | TCP port the gRPC server listens on (all interfaces). |
| `REDIS_URL` | `redis://localhost:26379/0` | Redis holding the inboxes, as a `redis://[:password@]host:port/db` URL. Only the address, database number and password are used: TLS and other URL options are not applied. Keep the password in your secret store and inject the URL at run time. |
| `IDENTITY_ADDR` | `localhost:9192` | `host:port` of the identity service's gRPC API, used to expand groups and label actors. The connection is plaintext. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (none) | OTLP gRPC endpoint for traces and metrics (plaintext). Unset or empty runs without a collector: no export, no error log. |
| `LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic` or `disabled`. |
| `LOG_FORMAT` | `json` | `json`, `console` (or `pretty`), or `both` (JSON on stdout, console on stderr). |

Local development logs at `trace` in `console` format (see `.env.example`). Cluster environments
log JSON, at `debug` in a dev cluster, `info` in QA or staging and `error` in production.

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
