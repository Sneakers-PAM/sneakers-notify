# API

The service implements `sneakers.notify.v1.NotifyService`, defined in
[proto/sneakers/notify/v1/notify.proto](../proto/sneakers/notify/v1/notify.proto). Go clients
import the generated code from `github.com/Sneakers-PAM/sneakers-notify/gen/go/sneakers/notify/v1`.

The server also registers the standard gRPC health service (`grpc.health.v1.Health`) and server
reflection.

A health check's answer carries the build in its response headers: `sneakers-version` (the image
tag, `dev` when unstamped) and `sneakers-commit` (the source commit, `unknown` when neither the
build nor Go's VCS stamp knows it). The gateway's diagnostics read them.

The health check has two services:

- `""` (the default) is readiness. It answers `NOT_SERVING` while Valkey, a required
  dependency, doesn't answer a `PING`, and `SERVING` again once it does. Each ping has a 1-second
  timeout and its result answers for 5 seconds, so frequent probes don't load Valkey. Its answer
  carries `sneakers-health`, a compact JSON report:

  ```json
  {"status":"degraded","dependencies":[{"name":"valkey","state":"ok","required":true,"checkedAt":"2026-10-05T12:00:05Z"},{"name":"identity","state":"degraded","required":false,"error":"unavailable","checkedAt":"2026-10-05T12:00:05Z"}]}
  ```

  `status` and each `state` are `ok`, `degraded` (an optional dependency is failing; still
  serving) or `down` (a required one is; not serving). `error` is a class, never the error
  itself: `timeout`, `refused`, `unavailable`, `unauthenticated` or `error`.
- `liveness` answers `SERVING` whenever the process does and never touches a dependency.

Any other service name is `NOT_FOUND`. `Watch` is not supported (`UNIMPLEMENTED`); poll `Check`.

Every call must carry the caller's workload identity: its projected Kubernetes ServiceAccount
token as `authorization: Bearer <token>` (see
[configuration.md](configuration.md#service-to-service-authentication)). Notify verifies it and
checks the caller against a per-method allow-list (`grpcsvc.CallerPolicy`):

| Caller | Methods | Access |
|---|---|---|
| `vault` | `NotifyEvent` | as itself |
| `gateway` | `ListNotifications`, `UnreadCount`, `MarkRead`, `MarkAllRead` | on behalf of the signed-in user (`user_id`) |

No or a bad token, or a service account that isn't in `WORKLOAD_ALLOWED_SERVICEACCOUNTS`, gets
`Unauthenticated`; a listed caller on a method it isn't listed for gets `PermissionDenied`. The
health service is exempt. A refusal is logged at warn (`call refused`, with the method, caller and
reason). The gateway still authenticates the end user and passes the signed-in user's id.

## RPCs

| RPC | What it does |
|---|---|
| `NotifyEvent` | Fans one event out to its recipients and returns how many inboxes it reached (`delivered`). |
| `ListNotifications` | A user's inbox, newest first. `limit` of 0, below 0 or above 100 means 100. |
| `UnreadCount` | The number of unread items in the user's inbox, for the badge. |
| `MarkRead` | Marks one item read. `ok` is false when the item isn't in the inbox (any more). |
| `MarkAllRead` | Marks every item in the user's inbox read. |

## How recipients are chosen

`NotifyEvent` carries `informed_subjects`, each with a `kind`:

- `user`: `name` is a user id and is added as is;
- `group`: `name` is a group name. It is matched against the identity service's groups
  (`ListGroups`, then `ListGroupMembers`) and also sent to `ListUsersByAdGroups`, which current
  identity releases answer with an empty list;
- `everyone`: ignored. Notify never broadcasts to every user.

The result is de-duplicated, and `actor_user_id` is removed from it, so nobody is notified of their
own action. A failed identity lookup skips that part of the expansion rather than failing the call.

Each item stores the action, the resource kind, id and label, the actor's display name (from
`ResolveUserLabels`, falling back to the actor's id) and `occurred_at` (the request's value, or the
server's time in RFC 3339 when it is empty).

`delivered` counts the inboxes written. A Redis error on one inbox is skipped and not counted; the
call still succeeds.

## Calling other services

The notify service never imports another service's Go module. It generates its own client stubs from
the callee's protos, pinned by commit:

- `proto-refs.env` pins the callee: `SNEAKERS_IDENTITY_REF=<commit>` for
  `Sneakers-PAM/sneakers-identity`.
- `scripts/proto-generate.sh` downloads only the callee's `proto/` at that commit into `.protos/`
  (git-ignored) and runs `buf generate`. The stubs land in `gen/go/thirdparty/identity/v1`, inside
  this module, so they can't collide with the owner's Go packages. The stubs are committed, so a
  build needs no network; the protos never are.
- To try an unmerged proto change, point `SNEAKERS_IDENTITY_PROTO_DIR` at a local `proto/` directory
  and run the script.
- To move to a newer callee, change its ref, run the script and commit `proto-refs.env` and `gen/`
  together. Build & Test fails when `gen/` doesn't match the pins.
- The `proto-sync` check (from `Sneakers-PAM/.github`) fails a PR whose pin isn't on the owner's
  `main` or that the owner's `main` breaks, and warns when `main` has moved on. On a schedule it
  opens a PR that bumps stale pins.
