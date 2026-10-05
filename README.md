# steward-obligations 📬

> 🧭 Obligations, acknowledgements, completion reports and notifications for Steward

The obligations service knows who must acknowledge which policy, records that they did, and tells
people what needs their attention.

- **Obligations:** who owes an acknowledgement on which published version, worked out from each
  policy's category rules (steward-authz), per-user overrides and sensitivity.
- **Acknowledgements:** recorded once per user and version, with the audit event in the same
  transaction; views; the account-merge transfer.
- **Reports:** completion, rosters, daily activity and an audit-quality export (CSV or JSON).
- **Notifications:** preferences per category and type with a compliance floor, digests, quiet
  hours in the user's own time zone, reminders with back-off and escalation, and delivery by email,
  in-app and push. Email is rendered by the sidecar in [`render/`](render/).

It calls core and identity, consumes their events and workflow's, and publishes steward-audit's
`AuditEvent`.

## 🚀 Run

```bash
cp .env.example .env   # a local Postgres, RabbitMQ, core, identity and the render sidecar
task run
```

Or build the images with `docker build --build-arg VERSION=dev --build-arg COMMIT=$(git rev-parse HEAD) -t steward-obligations .`
and the same in `render/`. Settings are in [configuration](docs/configuration.md); the probes are
in the [runbook](docs/runbook.md#probes).

## 📚 Docs

- [API](docs/api.md): the gRPC services, the events in and out, and calling other services.
- [Configuration](docs/configuration.md).
- [Runbook](docs/runbook.md).
- [Render sidecar](docs/render.md).
- [Error codes](docs/error-codes.md).

## 🛠 Develop

```bash
task build       # go build ./...
task test        # go test ./... (store and readiness tests start containers with testcontainers)
task lint        # gofmt check + golangci-lint + yamllint
task proto       # fetch the pinned callee protos, buf lint, regenerate gen/
```

Set `DATABASE_TEST_DSN` to run the store tests against an existing Postgres instead of a container.

## ⚖️ License

Apache-2.0 (c) 2026 The Steward Authors
