# Configuration

Everything comes from the environment. A bad setting stops the service at start-up with every
problem listed.

## Runtime

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_DSN` | required | Postgres. |
| `MIGRATE_DSN` | `DATABASE_DSN` | A direct connection for migrations, when `DATABASE_DSN` goes through a pooler. |
| `MIGRATIONS_DIR` | `migrations` | The baseline migration (`/migrations` in the image). |
| `RABBITMQ_URL` | required for start-up | The broker. |
| `GRPC_PORT` | `9090` | gRPC. |
| `PROBE_PORT` | `8080` | `/livez` and `/readyz`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | Traces and metrics. |
| `LOG_LEVEL`, `LOG_FORMAT` | go-log's | `trace` and `console` locally; clusters log JSON. |
| `GRPC_TLS_CERT_FILE`, `GRPC_TLS_KEY_FILE`, `GRPC_TLS_CLIENT_CA_FILE` | empty | mTLS, set together. |
| `OBLIGATIONS_TRUSTED_CALLERS` | empty | Comma-separated SPIFFE IDs whose forwarded actor is believed (needs mTLS). Without it acknowledgements are refused. |
| `CORE_GRPC_ADDR`, `IDENTITY_GRPC_ADDR` | required | The services called. |
| `REDIS_ADDR`, `REDIS_PASSWORD` | empty | Valkey for the obligating-set and email-service caches; empty turns them off. |
| `OBLIGATING_CACHE_TTL_SEC` | `60` | How long the obligating-policy set is cached. |

## Email

| Variable | Default | Meaning |
| --- | --- | --- |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD` | `587` for the port | The SMTP relay, used whenever the email service in core's settings is off. |
| `SMTP_FROM` | `no-reply@policies.example.org` | The sender. |
| `SMTP_STARTTLS`, `SMTP_TLS_INSECURE` | `true`, `false` | STARTTLS, and skipping the relay's certificate check. |
| `APP_ENV`, `DEV_MAIL_CATCHALL_TO` | empty | With `APP_ENV=dev`, every email goes to the catch-all address. |
| `RENDER_SIDECAR_URL` | `http://127.0.0.1:8091` | The [render sidecar](render.md). |
| `EMAIL_LOGO_URL`, `EMAIL_PRODUCT_NAME`, `EMAIL_LEGAL_TEXT` | empty | Adopter branding: a public logo URL, the product name ("Steward" when empty) and the footer's legal or postal line (none when empty). |
| `EMAIL_SERVICE_BREAKER_THRESHOLD`, `EMAIL_SERVICE_BREAKER_COOLDOWN_SEC` | `5`, `30` | Failures before the email-service transport counts as down, and the wait before trying it again. |
| `EMAIL_SERVICE_USAGE_SOFT_THRESHOLD`, `EMAIL_SERVICE_USAGE_HARD_CEILING`, `EMAIL_SERVICE_USAGE_POLL_SEC` | `0.80`, `0` (none), `300` | Plan-usage warnings, and the count past which mail is held. |
| `MAIL_OUTBOX_DRAIN_RATE` | `10` | Held messages sent per second once a transport works again. |
| `NOTIFY_UNSUB_SECRET` | empty | Signs the unsubscribe, preferences and email-verification links; empty turns all three off. |
| `NOTIFY_UNSUB_ENDPOINT_URL`, `NOTIFY_UNSUB_LINK_TTL_HOURS` | `PUBLIC_BASE_URL/notify/unsubscribe`, `720` | The one-click unsubscribe endpoint. |
| `NOTIFY_VERIFY_EMAIL_ENDPOINT_URL`, `NOTIFY_VERIFY_EMAIL_LINK_TTL_HOURS` | `PUBLIC_BASE_URL/notify/verify-email`, `48` | The email-verification endpoint. |
| `INTERNAL_BASE_URL` | `https://policies.example.org` | The staff app: the links in emails are built under it. |
| `PUBLIC_BASE_URL` | `INTERNAL_BASE_URL` | The public base for the unsubscribe and verification endpoints. |
| `SITE_ADMIN_EMAILS` | empty | Comma-separated addresses for the site-wide SSO notices. |

## Notifications

| Variable | Default | Meaning |
| --- | --- | --- |
| `FCM_PROJECT_ID` | empty | Turns push on. Push is off by default: it needs a messaging project per adopter. |
| `NEW_USER_ACK_THROTTLE_ENABLED`, `NEW_USER_ACK_GRACE_HOURS` | `false`, `24` | Holds a new account's first reminders back for the grace period. |
| `SCHEDULER_ENABLED`, `SCHEDULER_TICK_SEC` | `true`, `900` | The reminder sweep and digest drain; one replica runs each tick. |
| `ESCALATION_AFTER_DAYS` | `30` | When an unacknowledged obligation is escalated. |
