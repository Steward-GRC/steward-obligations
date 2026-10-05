# Runbook

## Start-up

The service applies the baseline migration and the audit outbox's own migration, connects to
Postgres and RabbitMQ, dials core and identity, starts the event consumers, the audit relay and (on
the scheduler leader) the reminder sweep and digest drain, and serves gRPC. A bad setting stops it
with every problem listed.

## Probes

Readiness follows go-buildinfo's dependency checker. Each check has a 2-second timeout, and a result
is reused for 5 seconds.

| Dependency | Required | When it's down |
| --- | --- | --- |
| `postgres` | yes | Not ready: nothing can be read or written. |
| `rabbitmq` | yes | Not ready: no event is consumed and no audit event is relayed. |
| `core`, `identity` | no | Degraded, still ready: obligation lookups and sends fail; acknowledgements, preferences and reports work. |
| `render` | no | Degraded, still ready: email is held in the mail outbox and sent once the sidecar is back. |
| `valkey` | no, reported when `REDIS_ADDR` is set | Degraded, still ready: reads go to core. If Valkey was unreachable at start-up, the caches stay off until a restart. |

- **HTTP on `PROBE_PORT` (8080):** `GET /livez` is 200 while the process is up and never checks a
  dependency. `GET /readyz` is 200 while ready and 503 while a required dependency is down; its
  JSON body lists every dependency with its state and error class. There's no plain `/health`.
- **gRPC on `GRPC_PORT`:** `grpc.health.v1` with the service name `liveness` reports the process
  only; the empty name and `readiness` follow readiness. Every `Health/Check` answer carries
  `steward-version`, `steward-commit`, `steward-dep-postgres` and `steward-depstate-<name>`.
- Never point liveness at a dependency: an outage would restart every replica.
- Readiness recovers on its own once the dependency is back.

## Common problems

| Symptom | Look at |
| --- | --- |
| `ACK_AUTH_REQUIRED` for every acknowledgement | The gateway isn't a trusted caller: check mTLS and `OBLIGATIONS_TRUSTED_CALLERS`. |
| `Code 7001: Internal Error` | A store call failed; the log line with the same trace id names the `op`. |
| Audit events stop arriving | `SELECT status, count(*) FROM audit_outbox GROUP BY status`: `pending` rows mean the relay can't publish (check RabbitMQ), `dead` rows have their last error kept. |
| Email isn't going out | `mail_outbox` rows with `status = 'pending'` are held: no working transport, the email service over its limit, or the render sidecar down. |
| Quiet hours or digests at the wrong hour | The user's time zone in identity; unset falls back to UTC. |
| No push | `FCM_PROJECT_ID` is unset, which is the default. |

## Backups

Back up Postgres. Acknowledgements are attestations; the audit chain in steward-audit holds their
evidence.
