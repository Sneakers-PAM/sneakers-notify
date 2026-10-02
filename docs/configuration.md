# Configuration

The service reads its configuration from the environment. Every setting has a default.

| Variable | Default | Purpose |
|---|---|---|
| `GRPC_PORT` | `9090` | TCP port the gRPC server listens on (all interfaces). |
| `REDIS_URL` | `redis://localhost:26379/0` | Redis holding the inboxes, as a `redis://[:password@]host:port/db` URL. Only the address, database number and password are used: TLS and other URL options are not applied. Keep the password in your secret store and inject the URL at run time. |
| `IDENTITY_ADDR` | `localhost:9192` | `host:port` of the identity service's gRPC API, used to expand groups and label actors. The connection is plaintext. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | OTLP gRPC endpoint for traces and metrics (plaintext). |
| `LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic` or `disabled`. |
| `LOG_FORMAT` | `json` | `json`, `console` (or `pretty`), or `both` (JSON on stdout, console on stderr). |

Local development logs at `trace` in `console` format (see `.env.example`). Cluster environments
log JSON, at `debug` in a dev cluster, `info` in QA or staging and `error` in production.

## Fixed limits

These are constants in the code, not settings:

- each user's inbox keeps the newest 100 items;
- an inbox expires 90 days after its last new item;
- `ListNotifications` returns at most 100 items, whatever `limit` asks for.
