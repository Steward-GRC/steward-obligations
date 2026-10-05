# API

The API is `steward.obligations.v1`, in
[`proto/steward/obligations/v1`](../proto/steward/obligations/v1), with the Go stubs committed in
`gen/go`. Errors carry an `ErrorInfo` ([error codes](error-codes.md)).

| Service | Holds |
| --- | --- |
| `AckService` | `RecordAck` and `RecordView` for the forwarded actor (the request names only the version), `GetAckStatus`, and `TransferAcknowledgments` for an account merge: deduplicated, idempotent, with a dry run for the preview. A real transfer must name `actor_user_id`. |
| `ObligationService` | `GetMyObligations`, `MyAckSummary`, `GetObligatedAudienceCount`. |
| `ReportingService` | `GetCompletionReport`, `GetAckRoster`, `GetAckActivity`, `ExportAcks` (CSV or JSON). |
| `NotifPrefService` | Channel switches, the notification-type catalogue, category and type cadences (off is refused for mandatory categories), and the digest window. |
| `WelcomeService` | `ResendWelcome`, an admin action. |

## Act-as

The caller of `RecordAck` and `RecordView` is the forwarded actor (go-grpc-actor). It is believed
only from a trusted caller (`OBLIGATIONS_TRUSTED_CALLERS` over mTLS); otherwise the call has no
actor and is refused with `ACK_AUTH_REQUIRED`. During act-as the acknowledgement is the target's,
and its audit event names the real admin, keeping the target in `impersonated_user_id`.

## Health

The server serves `grpc.health.v1` and reflection. `liveness` reports the process only; the empty
name and `readiness` follow the required dependencies. Every `Health/Check` answer carries
`steward-version`, `steward-commit`, `steward-dep-postgres` and `steward-depstate-<name>`.
`/livez` and `/readyz` serve the same over HTTP on `PROBE_PORT`; see the
[runbook](runbook.md#probes).

## Events in

Durable queues on the `jobs` topic exchange, JSON bodies in their producers' contracts. A failed
delivery is requeued.

| Queue | Routing key | From | Does |
| --- | --- | --- | --- |
| `obligations.policy.published` | `policy.published` | core | Notifies the audience that hasn't acknowledged the new version. |
| `obligations.policy.retired` | `policy.retired` | core | Tells the audience the policy is retired; acknowledgements are kept. |
| `obligations.policy.obligation_changed` | `policy.obligation_changed` | core | Re-resolves the audience and removes acknowledgements no longer owed. |
| `obligations.membership.changed` | `membership.changed` | identity | The same for a user who left a group. |
| `obligations.account.created` | `account.created` | identity | The welcome email, once per account. |
| `obligations.account.created.verify` | `account.created` | identity | The email-verification email (only with `NOTIFY_UNSUB_SECRET`). |
| `obligations.sso.lifecycle` | `sso.lifecycle` | identity | SSO account, access and admin notices. |
| `obligations.auth.recovery` | `auth.recovery` | gateway | The account-recovery code email. |
| `obligations.workflow.approval_requested` | `workflow.approval_requested` | workflow | Asks the approver, once per task. |
| `obligations.workflow.started`, `obligations.workflow.denied` | `workflow.started`, `workflow.denied` | workflow | Tells the submitter. |

## Events out

Every audit event is a `steward.audit.v1.AuditEvent` (steward-audit's contract), written to the
`audit_outbox` table and relayed by go-outbox to the `audit` topic exchange as protobuf binary,
content type `application/protobuf; proto=steward.audit.v1.AuditEvent`, routing key `audit.audit`.
An acknowledgement and its `ack.recorded` event commit together, so neither exists without the
other. Other actions: `ack.transferred`, the publish audience summary, and each email sent.

## Calling other services

The service calls core and identity and uses audit's event contract. It never imports their Go
modules: each is pinned by commit in [`proto-refs.env`](../proto-refs.env), and
`scripts/proto-generate.sh` fetches only their `proto/` packages into the git-ignored `.protos/` and
generates `gen/go/thirdparty/{core,identity,audit}/v1`. Only the stubs are committed. To try an
unmerged change, set `STEWARD_CORE_PROTO_DIR`, `STEWARD_IDENTITY_PROTO_DIR` or
`STEWARD_AUDIT_PROTO_DIR` to that repo's `proto/` directory.

| Callee | RPCs |
| --- | --- |
| core | `PolicyService` (`GetPolicy`, `GetPolicyVersion`, `ListPolicyVersions`, `ResolvePolicyObligation`, `ListObligatingPolicies`), `CategoryService` (`GetCategory`, `GetCategoryRuleset`: the category chain), `EmailServiceSecretService` (the email-service settings). |
| identity | `IdentityReadService` (`GetUser`, `ResolveEmail`, `ResolveFCMToken`, `ListAllUsers`). |
