# Notify Service 🔔

> 📬 The Sneakers-PAM notification inbox: who should hear about an event, and what each user hasn't read yet.

A gRPC service, `sneakers.notify.v1.NotifyService`, backed by Redis. When something happens to a
secret or folder, the caller sends notify the event and the subjects that should be kept informed
(users and groups). Notify asks the identity service who those people are, drops the actor, and
puts one item in each recipient's inbox. The UI then reads the inbox, the unread badge, and marks
items read.

It is best-effort by design. Inboxes are capped and expire, and a lost item is acceptable: the
audit trail is the durable record of every event, not notify.

## ✨ Highlights

- 👥 **Fan-out:** users by id, groups by name, resolved through the identity API; the actor is never notified of their own action.
- 📥 **Inbox:** the newest 100 items per user, kept for 90 days, with an unread count derived from the list itself.
- 🧹 **Light footprint:** no SQL database and no schema; one Redis and one identity endpoint.

## 🚀 Run it

```bash
docker run -d --name notify-redis -p 127.0.0.1:6379:6379 redis:7-alpine
WORKLOAD_AUTH=disabled REDIS_URL=redis://localhost:6379/0 IDENTITY_ADDR=localhost:9192 \
  go run ./cmd/notify
```

`WORKLOAD_AUTH=disabled` lets any local caller in without a workload token, for development only.
In a cluster notify accepts only the vault and the gateway, by their ServiceAccount tokens, and
sends its own token to identity. The service listens for gRPC on port 9090. It needs Redis at start; the identity service is only
called when an event names a group or when it labels the actor, and a failed lookup is skipped.

Run the tests (the store tests use an in-process Redis, so nothing else is needed):

```bash
go test ./...
```

## 🛠 Develop

```bash
task build    # go build ./...
task test     # go test ./...
task lint     # tests, gofmt check, golangci-lint and yamllint
task license  # check the Apache-2.0 headers (golic)
```

## 📚 Where to look

- [docs/configuration.md](docs/configuration.md): environment variables.
- [docs/api.md](docs/api.md): the gRPC API.
- [docs/runbook.md](docs/runbook.md): operating the service.
- [proto/sneakers/notify/v1/notify.proto](proto/sneakers/notify/v1/notify.proto): the API definition.

## 🙏 Acknowledgements

Sneakers-PAM was originally written by [@Bugs5382](https://github.com/Bugs5382).

## ⚖️ License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
