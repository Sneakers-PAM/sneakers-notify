# AGENTS.md - sneakers-notify

Guide for AI agents working in this repository. Pair with `CLAUDE.md` (the working agreement and
hook-enforced rules). Keep this file current when the build, layout, or public API changes.

## What this is

Sneakers notify service: the best-effort notification inbox. A gRPC service
(`sneakers.notify.v1.NotifyService`) over Redis that fans an event out to the users who should be
kept informed (resolving groups through the identity service) and serves each user's inbox and
unread count. Before changing it, know that it is best-effort on purpose: inboxes are capped and
expire, lookups that fail are skipped rather than failing the call, and the audit trail, not
notify, is the record of every event. Keep it that way.

## Layout

- `cmd/notify/` - the service entrypoint: environment, Redis, the identity client, the gRPC server.
- `internal/grpcsvc/` - the service and its handler tests.
- `internal/fanout/` - turns Informed subjects into recipient ids and labels the actor, over the
  identity API.
- `internal/store/` - the Redis inbox (one capped, expiring list per user) and its tests.
- `internal/safeconv/` - the bounds-checked int to int32 conversion.
- `internal/server/` - the gRPC server bootstrap, with the health service and readiness checks
  from `github.com/Bugs5382/go-buildinfo`.
- Service-to-service authentication comes from `github.com/Bugs5382/go-workload-identity`;
  `internal/server/workloadauth.go` sets the Sneakers audience and caller-name prefix and builds
  the interceptors. The allow-list is `internal/grpcsvc/callers.go`; a new RPC needs an entry there.
- `proto/` - the API; `gen/go/` - the generated Go (committed, checked current in CI).
- `docs/` - configuration, API and runbook.

## Build, test, lint

- Build: `task build`
- Test: `task test`; the store tests start an in-process Redis, so nothing else is needed.
- Lint: `task lint`, plus `buf lint` for the proto (after `scripts/proto-generate.sh` has
  fetched the identity protos).
- Generated code: `scripts/proto-generate.sh`, with the plugin versions pinned in
  `.github/workflows/job-go-lang-ci.yaml`.
- Vulnerabilities: `task vuln` runs govulncheck as CI does (`scripts/govulncheck.sh`): any called
  finding fails unless its ID is in `govulncheck-allow.txt`, which says why and when each entry
  goes. `scripts/govulncheck_test.sh` checks the filter itself.
- License headers: `task license` (golic, the Apache-2.0 SPDX header in `.golic.yaml`).

## Logging

Follow the logging rules in `CLAUDE.md`. In short:

- Log generously: entry and exit of significant operations, decisions and branches, retries, state
  changes, external calls (target, duration, outcome), and every error with its context.
- Levels: `trace` for step-by-step detail, `debug` for flow, `info` for lifecycle, `warn` and
  `error` for problems. The environment filters the volume, so err on the side of too much.
- Environments: local dev `trace` with `LOG_FORMAT=console` (never JSON), dev cluster `debug`,
  qa/staging `info`, production `error`. Every cluster environment logs JSON. Set levels through
  `LOG_LEVEL` and `LOG_FORMAT`, never in code; local settings live in the run target or
  `.env.example`.
- Never log secrets, tokens, or personal data, not even at `trace`. Log an opaque or keyed ID.

## Conventions and gotchas

- See `CLAUDE.md` for the branch/commit/PR rules; they are enforced by the git hooks in
  `.claude/hooks` (run `bash .claude/hooks/install.sh` once per clone).
- Open every PR as a draft. CI skips drafts, so run the full checks locally, push once they pass,
  and mark the PR ready when the work is finished; see CLAUDE.md "CI and Actions minutes".
- Every commit carries a DCO sign-off (`git commit -s`); the `checks / scrub` job fails without it.
- No real identifiers anywhere: fixtures use example.org, 192.0.2.0/24, 2001:db8::/32 and invented
  names.
- The identity client stubs in `gen/go/thirdparty/identity/v1` are generated from the commit
  pinned in `proto-refs.env` (see docs/api.md, "Calling other services"); never import another
  service's Go module. `ListUsersByAdGroups` is deprecated in the identity API (it answers
  empty); the lint config skips that one deprecation warning.
- `go.mod` holds tagged releases only: no `replace` directive, and no pseudo-version (`@main`,
  `@<sha>`) of a `github.com/Bugs5382/*` or `github.com/Sneakers-PAM/*` module; the
  `proto-sync / check` job fails on either. To compile and test against a local package checkout,
  use a git-ignored `go.work` beside `go.mod` (`go work init . ../go-<pkg>`, which writes
  `use . ../go-<pkg>`); `go.work` and `go.work.sum` are in `.gitignore`. For local callee protos,
  point `SNEAKERS_IDENTITY_PROTO_DIR` at a local `proto/` directory when running
  `scripts/proto-generate.sh`, rather than editing a pin in `proto-refs.env`.
